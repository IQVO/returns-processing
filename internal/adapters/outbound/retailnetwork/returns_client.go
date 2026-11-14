// Package retailnetwork holds the outbound adapter for the external retail
// network's Vendor API (ADR 0012) — this fleet context's FIRST
// vendor-connectivity client. The package is the only place in the service
// that may name the network's vocabulary: nothing above it — no use case,
// no domain type, no other adapter — names an authorization id, a vendor
// id, a node id or a transaction id.
//
// The pattern is the purchase-order integration's: poll a work seam, map
// the network's identifiers onto our vocabulary at the edge, and make
// writes idempotent by a derived key so a retry after a crash can never
// double-report. There is no credential and no auth header; the network
// knows the caller by its vendor id.
package retailnetwork

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// advicePageLimit is the page size of the poll (the network caps pages; the
// client walks every page of one poll).
const advicePageLimit = 100

// returnsClient talks to the Vendor API's return seam: the advices poll,
// the outcome submission and the transaction reads.
type returnsClient struct {
	baseURL  string
	vendorID string
	http     *http.Client
	logger   *slog.Logger
}

func newReturnsClient(baseURL, vendorID string, httpClient *http.Client, logger *slog.Logger) (*returnsClient, error) {
	if baseURL == "" {
		return nil, errors.New("retail-network: NETWORK_RETURNS_BASE_URL is required")
	}
	if vendorID == "" {
		return nil, errors.New("retail-network: NETWORK_RETURNS_VENDOR_ID is required")
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("retail-network: NETWORK_RETURNS_BASE_URL is invalid: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &returnsClient{baseURL: baseURL, vendorID: vendorID, http: httpClient, logger: logger}, nil
}

// adviceDTO is one polled advice, the network's wire shape.
type adviceDTO struct {
	AuthorizationID string    `json:"authorizationId"`
	OrderID         string    `json:"orderId"`
	GroupNo         int64     `json:"groupNo"`
	VendorID        string    `json:"vendorId"`
	NodeID          string    `json:"nodeId"`
	Disposition     string    `json:"disposition"`
	Method          string    `json:"method"`
	State           string    `json:"state"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Lines           []struct {
		LineNo        int32  `json:"lineNo"`
		CatalogItemID string `json:"catalogItemId"`
		Quantity      int64  `json:"quantity"`
		OwnerRef      string `json:"ownerRef"`
	} `json:"lines"`
	TrackingNumbers []string  `json:"trackingNumbers"`
	ReadyAt         time.Time `json:"readyAt"`
}

type advicePageDTO struct {
	Items      []adviceDTO `json:"items"`
	NextCursor string      `json:"nextCursor"`
}

// outcomeRequestDTO is the submission body.
type outcomeRequestDTO struct {
	VendorID        string           `json:"vendorId"`
	AuthorizationID string           `json:"authorizationId"`
	NodeID          string           `json:"nodeId"`
	ReceivedAt      time.Time        `json:"receivedAt"`
	Lines           []outcomeLineDTO `json:"lines"`
}

type outcomeLineDTO struct {
	CatalogItemID  string `json:"catalogItemId"`
	OwnerRef       string `json:"ownerRef,omitempty"`
	QtyRestockable int64  `json:"qtyRestockable"`
	QtyUnsellable  int64  `json:"qtyUnsellable"`
}

type transactionDTO struct {
	TransactionID string               `json:"transactionId"`
	Status        string               `json:"status"`
	Errors        []submissionErrorDTO `json:"errors"`
}

// submissionErrorDTO is one problem entry of a FAILURE transaction.
type submissionErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// problemDTO is the network's error document (problem+json).
type problemDTO struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// pollAdvices walks every page of one poll: the advices with readyAt
// strictly after readyAfter (the zero time polls from the beginning).
func (c *returnsClient) pollAdvices(ctx context.Context, readyAfter time.Time) ([]adviceDTO, error) {
	q := url.Values{}
	q.Set("vendorId", c.vendorID)
	q.Set("limit", strconv.Itoa(advicePageLimit))
	if !readyAfter.IsZero() {
		q.Set("readyAfter", readyAfter.UTC().Format(time.RFC3339Nano))
	}
	var out []adviceDTO
	cursor := ""
	for page := 0; ; page++ {
		pageQuery := q
		if cursor != "" {
			pageQuery = url.Values{}
			for k, v := range q {
				pageQuery[k] = v
			}
			pageQuery.Set("cursor", cursor)
		}
		var body advicePageDTO
		if err := c.getJSON(ctx, "/vendor/return-advices", pageQuery, &body); err != nil {
			return nil, err
		}
		out = append(out, body.Items...)
		if body.NextCursor == "" || page >= 100 {
			return out, nil
		}
		cursor = body.NextCursor
	}
}

// submitOutcome posts the graded result once; key is the derived
// idempotency key (the same outcome always carries the same key, so a
// retry replays the network's first answer instead of writing twice).
func (c *returnsClient) submitOutcome(ctx context.Context, out views.ReturnOutcome) (string, error) {
	body := outcomeRequestDTO{
		VendorID: c.vendorID, AuthorizationID: out.AuthorizationID, NodeID: out.NodeID,
		ReceivedAt: out.ReceivedAt, Lines: make([]outcomeLineDTO, len(out.Lines)),
	}
	for i, l := range out.Lines {
		body.Lines[i] = outcomeLineDTO{CatalogItemID: l.CatalogItemID, OwnerRef: l.OwnerRef, QtyRestockable: l.QtyRestockable, QtyUnsellable: l.QtyUnsellable}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	var accepted struct {
		TransactionID string `json:"transactionId"`
	}
	if err := c.do(ctx, http.MethodPost, "/vendor/return-outcomes", url.Values{"vendorId": {c.vendorID}}, raw, outcomeKey(out.AuthorizationID), &accepted); err != nil {
		return "", err
	}
	if accepted.TransactionID == "" {
		return "", fmt.Errorf("retail-network: the outcome submission of %s was accepted without a transaction id", out.AuthorizationID)
	}
	return accepted.TransactionID, nil
}

// transaction reads the network's verdict of an asynchronous write.
func (c *returnsClient) transaction(ctx context.Context, transactionID string) (transactionDTO, error) {
	var tx transactionDTO
	err := c.getJSON(ctx, "/vendor/transactions/"+url.PathEscape(transactionID), url.Values{"vendorId": {c.vendorID}}, &tx)
	return tx, err
}

// outcomeKey derives the idempotency key of an advice's outcome: printable
// ASCII, stable, one per authorization (a retry after a crash re-sends the
// same key and body, so the network replays its first answer).
func outcomeKey(authorizationID string) string { return "return-outcome-" + authorizationID }

func (c *returnsClient) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, "", out)
}

// do makes one Vendor API call. A definitive answer — the network replied
// 400/404/409/422 with a problem document — wraps ports.ErrOutcomeRefused
// (the drain records it terminal); 5xx and transport failures are plain
// errors the next tick retries.
func (c *returnsClient) do(ctx context.Context, method, path string, query url.Values, body []byte, idempotencyKey string, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path+"?"+query.Encode(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("retail-network: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("retail-network: %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("retail-network: %s %s: %w", method, path, err)
		}
		return nil
	}
	return c.refusal(method, path, resp.StatusCode, raw)
}

// refusal maps a non-2xx answer onto the two error classes the drain
// knows: definitive (wrapping ports.ErrOutcomeRefused) and retriable.
func (c *returnsClient) refusal(method, path string, status int, raw []byte) error {
	var p problemDTO
	_ = json.Unmarshal(raw, &p)
	detail := p.Detail
	if detail == "" {
		detail = p.Title
	}
	if detail == "" {
		detail = string(raw)
	}
	err := fmt.Errorf("retail-network: %s %s answered %d %s: %s", method, path, status, p.Type, detail)
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", ports.ErrOutcomeRefused, err)
	default:
		return err
	}
}
