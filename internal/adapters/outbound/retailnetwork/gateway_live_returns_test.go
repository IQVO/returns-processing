package retailnetwork

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

const adviceJSON = `{
	"authorizationId": "ret-1001", "orderId": "ord-100", "groupNo": 1,
	"vendorId": "V-WS", "nodeId": "n-1",
	"disposition": "NEAREST_RETURNS_CENTER", "method": "DROP_OFF", "state": "AUTHORIZED",
	"expiresAt": "2026-12-14T10:00:00Z",
	"lines": [{"lineNo": 1, "catalogItemId": "ci-100", "quantity": 2, "ownerRef": "seller-7"}],
	"trackingNumbers": ["TRK-RET-9"],
	"readyAt": "2026-11-14T10:00:00Z"
}`

// vendorAPISpy scripts the network's three seam routes and records what the
// gateway sent.
type vendorAPISpy struct {
	mu      sync.Mutex
	server  *httptest.Server
	polls   []string // every poll's raw query
	submits []string // every submission's raw body
	keys    []string // every submission's Idempotency-Key
	pages   func(page int) (string, string)
	submit  func() (int, string)
	txBody  string
}

func newVendorAPISpy(t *testing.T) *vendorAPISpy {
	t.Helper()
	spy := &vendorAPISpy{
		pages:  func(int) (string, string) { return `{"items":[]}`, "" },
		submit: func() (int, string) { return http.StatusAccepted, `{"transactionId":"tx-1"}` },
		txBody: `{"transactionId":"tx-1","status":"PROCESSING","errors":[]}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/vendor/return-advices", func(w http.ResponseWriter, r *http.Request) {
		spy.mu.Lock()
		page := len(spy.polls)
		spy.polls = append(spy.polls, r.URL.RawQuery)
		spy.mu.Unlock()
		body, next := spy.pages(page)
		if next != "" {
			body = strings.TrimSuffix(body, "}") + `,"nextCursor":"` + next + `"}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("/vendor/return-outcomes", func(w http.ResponseWriter, r *http.Request) {
		spy.mu.Lock()
		spy.keys = append(spy.keys, r.Header.Get("Idempotency-Key"))
		spy.mu.Unlock()
		buf := make([]byte, r.ContentLength)
		if n, _ := r.Body.Read(buf); n > 0 {
			spy.mu.Lock()
			spy.submits = append(spy.submits, string(buf))
			spy.mu.Unlock()
		}
		status, body := spy.submit()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("/vendor/transactions/tx-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, spy.txBody)
	})
	spy.server = httptest.NewServer(mux)
	t.Cleanup(spy.server.Close)
	return spy
}

func (s *vendorAPISpy) gateway(t *testing.T) *LiveReturnsGateway {
	t.Helper()
	g, err := NewLiveReturnsGateway(ReturnsConfig{BaseURL: s.server.URL, VendorID: "V-WS"})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	return g
}

func TestLiveReturnsGatewayPollsEveryPage(t *testing.T) {
	spy := newVendorAPISpy(t)
	spy.pages = func(page int) (string, string) {
		if page == 0 {
			return `{"items":[` + adviceJSON + `]}`, "cursor-2"
		}
		return `{"items":[` + adviceJSON + `]}`, "" // the same advice again on page 2
	}
	advices, err := spy.gateway(t).PollAdvices(context.Background(), time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(advices) != 1 || advices[0].AuthorizationID != "ret-1001" {
		t.Fatalf("advices = %+v", advices)
	}
	assertAdviceMapping(t, advices[0])
	assertPollQueries(t, spy)
}

// assertAdviceMapping checks the wire-to-vocabulary mapping of the fixture
// advice (split from the page test to keep each under the complexity gate).
func assertAdviceMapping(t *testing.T, a views.ReturnAdvice) {
	t.Helper()
	if a.NodeID != "n-1" || a.Disposition != "NEAREST_RETURNS_CENTER" || len(a.Lines) != 1 || a.Lines[0].OwnerRef != "seller-7" || len(a.TrackingNumbers) != 1 {
		t.Fatalf("mapped advice = %+v", a)
	}
	if !a.ReadyAt.Equal(time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("readyAt = %v", a.ReadyAt)
	}
}

// assertPollQueries checks the filters of every page of one poll.
func assertPollQueries(t *testing.T, spy *vendorAPISpy) {
	t.Helper()
	// Page 1 carries the filters; page 2 repeats them plus the cursor.
	if len(spy.polls) != 2 || !strings.Contains(spy.polls[0], "vendorId=V-WS") || !strings.Contains(spy.polls[0], "limit=") {
		t.Fatalf("polls = %v", spy.polls)
	}
	if !strings.Contains(spy.polls[1], "cursor=cursor-2") || !strings.Contains(spy.polls[1], "vendorId=V-WS") {
		t.Fatalf("page 2 query = %v", spy.polls[1])
	}
}

func TestLiveReturnsGatewayPollSendsReadyAfter(t *testing.T) {
	spy := newVendorAPISpy(t)
	since := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	if _, err := spy.gateway(t).PollAdvices(context.Background(), since); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spy.polls[0], "readyAfter=2026-11-14T08%3A00%3A00Z") {
		t.Fatalf("poll query = %v", spy.polls[0])
	}
}

func TestLiveReturnsGatewaySubmitsOutcomeIdempotently(t *testing.T) {
	spy := newVendorAPISpy(t)
	g := spy.gateway(t)
	out := views.ReturnOutcome{
		AuthorizationID: "ret-1001", NodeID: "n-1",
		ReceivedAt: time.Date(2026, 11, 14, 12, 30, 0, 0, time.UTC),
		Lines: []views.ReturnOutcomeLine{
			{CatalogItemID: "ci-100", QtyRestockable: 2, QtyUnsellable: 1},
			{CatalogItemID: "ci-100", OwnerRef: "seller-7", QtyUnsellable: 3},
		},
	}
	txID, err := g.SubmitReturnOutcome(context.Background(), out)
	if err != nil || txID != "tx-1" {
		t.Fatalf("submit = %q err=%v", txID, err)
	}
	// The derived key: stable per authorization, printable ASCII.
	if len(spy.keys) != 1 || spy.keys[0] != "return-outcome-ret-1001" {
		t.Fatalf("keys = %v", spy.keys)
	}
	body := spy.submits[0]
	for _, want := range []string{`"vendorId":"V-WS"`, `"authorizationId":"ret-1001"`, `"nodeId":"n-1"`, `"receivedAt":"2026-11-14T12:30:00Z"`, `"qtyRestockable":2`, `"qtyUnsellable":3`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s misses %s", body, want)
		}
	}
	if strings.Contains(body, `"ownerRef":""`) { // omitted owner stays omitted
		t.Fatalf("body %s carries an empty ownerRef", body)
	}
	// A retry sends the same key (the network replays its first answer).
	if _, err := g.SubmitReturnOutcome(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	if spy.keys[1] != spy.keys[0] {
		t.Fatalf("keys drifted: %v", spy.keys)
	}
}

func TestLiveReturnsGatewayRefusalClasses(t *testing.T) {
	spy := newVendorAPISpy(t)
	g := spy.gateway(t)
	out := views.ReturnOutcome{AuthorizationID: "ret-1001", NodeID: "n-1", ReceivedAt: time.Unix(1, 0), Lines: []views.ReturnOutcomeLine{{CatalogItemID: "ci-1", QtyUnsellable: 1}}}

	// A definitive answer (422 with a problem document) wraps ErrOutcomeRefused.
	spy.submit = func() (int, string) {
		return http.StatusUnprocessableEntity, `{"type":"validation-failed","title":"Validation Failed","detail":"node is not the advice's"}`
	}
	_, err := g.SubmitReturnOutcome(context.Background(), out)
	if !errors.Is(err, ports.ErrOutcomeRefused) {
		t.Fatalf("422 = %v", err)
	}
	// A 429 is retriable: a plain error, not a verdict.
	spy.submit = func() (int, string) { return http.StatusTooManyRequests, `{"type":"rate-limited"}` }
	_, err = g.SubmitReturnOutcome(context.Background(), out)
	if err == nil || errors.Is(err, ports.ErrOutcomeRefused) {
		t.Fatalf("429 = %v", err)
	}
}

func TestLiveReturnsGatewaySettlesTransactions(t *testing.T) {
	spy := newVendorAPISpy(t)
	g := spy.gateway(t)
	ctx := context.Background()

	spy.txBody = `{"transactionId":"tx-1","status":"PROCESSING","errors":[]}`
	if tx, err := g.OutcomeTransaction(ctx, "tx-1"); err != nil || tx.Status != views.TransactionProcessing {
		t.Fatalf("processing = %+v err=%v", tx, err)
	}
	spy.txBody = `{"transactionId":"tx-1","status":"SUCCESS","errors":[]}`
	if tx, err := g.OutcomeTransaction(ctx, "tx-1"); err != nil || tx.Status != views.TransactionSucceeded {
		t.Fatalf("success = %+v err=%v", tx, err)
	}
	spy.txBody = `{"transactionId":"tx-1","status":"FAILURE","errors":[{"code":"node-mismatch","message":"n-9"}]}`
	tx, err := g.OutcomeTransaction(ctx, "tx-1")
	if err != nil || tx.Status != views.TransactionFailed || tx.Errors != "node-mismatch: n-9" {
		t.Fatalf("failure = %+v err=%v", tx, err)
	}
}

func TestNewLiveReturnsGatewayRequiresWiring(t *testing.T) {
	if _, err := NewLiveReturnsGateway(ReturnsConfig{VendorID: "V-WS"}); err == nil || !strings.Contains(err.Error(), "NETWORK_RETURNS_BASE_URL") {
		t.Fatalf("no base url = %v", err)
	}
	if _, err := NewLiveReturnsGateway(ReturnsConfig{BaseURL: "http://network"}); err == nil || !strings.Contains(err.Error(), "NETWORK_RETURNS_VENDOR_ID") {
		t.Fatalf("no vendor = %v", err)
	}
}
