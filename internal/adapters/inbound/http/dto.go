package http

import (
	"time"

	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// formatTime renders t as RFC 3339 (Nano) with a UTC Z, or "" when t is the
// zero time — optional instants are omitted, never emitted as year 1.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// returnDTO is the wire shape of one RMA (apis/openapi.yaml, Return).
type returnDTO struct {
	RMAID         string `json:"rmaId"`
	OrderID       string `json:"orderId"`
	LineNo        int32  `json:"lineNo"`
	SKU           string `json:"sku"`
	Quantity      int64  `json:"quantity"`
	SiteID        string `json:"siteId"`
	ReasonCode    string `json:"reasonCode,omitempty"`
	RejectionCode string `json:"rejectionCode,omitempty"`
	Status        string `json:"status"`
	WindowEndsAt  string `json:"windowEndsAt,omitempty"`
	Version       int64  `json:"version"`
}

// returnDTOFrom projects the application's read view.
func returnDTOFrom(v views.ReturnView) returnDTO {
	return returnDTO{
		RMAID:         v.ID,
		OrderID:       v.OrderID,
		LineNo:        v.LineNo,
		SKU:           v.SKU,
		Quantity:      v.Quantity,
		SiteID:        v.SiteID,
		ReasonCode:    v.ReasonCode,
		RejectionCode: v.RejectionCode,
		Status:        v.Status,
		WindowEndsAt:  formatTime(v.WindowEndsAt),
		Version:       v.Version,
	}
}

// receiptLineDTO is the wire shape of one received line (ReceiptLine).
type receiptLineDTO struct {
	LineNo            int32    `json:"lineNo"`
	SKU               string   `json:"sku"`
	Quantity          int64    `json:"quantity"`
	Kind              string   `json:"kind"`
	RMAID             string   `json:"rmaId,omitempty"`
	Inspected         bool     `json:"inspected"`
	InspectedQuantity int64    `json:"inspectedQuantity,omitempty"`
	Grade             string   `json:"grade,omitempty"`
	ReasonCodes       []string `json:"reasonCodes,omitempty"`
	InspectedAt       string   `json:"inspectedAt,omitempty"`
}

// receiptDTO is the wire shape of one receipt (Receipt). rmaId is nullable:
// null when the whole parcel was unexpected.
type receiptDTO struct {
	ReceiptID string           `json:"receiptId"`
	RMAID     *string          `json:"rmaId"`
	SiteID    string           `json:"siteId"`
	OpenedAt  string           `json:"openedAt"`
	Lines     []receiptLineDTO `json:"lines"`
	Version   int64            `json:"version"`
}

// receiptDTOFrom projects the aggregate the use case returns.
func receiptDTOFrom(r *returnreceipt.Receipt) receiptDTO {
	dto := receiptDTO{
		ReceiptID: r.ID(),
		SiteID:    r.SiteID(),
		OpenedAt:  formatTime(r.OpenedAt()),
		Lines:     make([]receiptLineDTO, 0, len(r.Lines())),
		Version:   r.Version(),
	}
	if rma := r.RMAID(); rma != "" {
		dto.RMAID = &rma
	}
	for _, l := range r.Lines() {
		line := receiptLineDTO{
			LineNo:    l.LineNo(),
			SKU:       l.SKU(),
			Quantity:  l.Quantity(),
			Kind:      string(l.Kind()),
			Inspected: l.Inspected(),
		}
		if l.RMAID() != "" {
			line.RMAID = l.RMAID()
		}
		if l.Inspected() {
			line.InspectedQuantity = l.InspectedQuantity()
			line.Grade = l.Grade().String()
			line.ReasonCodes = l.ReasonCodes()
			// The domain Line exposes no inspected-at accessor; a receipt
			// fresh from Open (the only receipt this DTO renders) has no
			// inspected line, so the instant is simply absent.
		}
		dto.Lines = append(dto.Lines, line)
	}
	return dto
}

// dispositionDTO is the wire shape of one decision (Disposition). supersedes
// and reason are present on an override only.
type dispositionDTO struct {
	DispositionID string `json:"dispositionId"`
	ReceiptID     string `json:"receiptId"`
	LineNo        int32  `json:"lineNo"`
	SKU           string `json:"sku"`
	Quantity      int64  `json:"quantity"`
	Grade         string `json:"grade"`
	Outcome       string `json:"outcome"`
	PolicyVersion int64  `json:"policyVersion"`
	Supersedes    string `json:"supersedes,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Staged        bool   `json:"staged"`
	DecidedAt     string `json:"decidedAt"`
	Version       int64  `json:"version"`
}

// dispositionDTOFrom projects the aggregate the use case returns. version
// rides the snapshot (the persisted form) — the aggregate holds no accessor
// of its own.
func dispositionDTOFrom(d *disposition.Disposition) dispositionDTO {
	snap := d.Snapshot()
	return dispositionDTO{
		DispositionID: d.ID(),
		ReceiptID:     d.ReceiptID(),
		LineNo:        d.LineNo(),
		SKU:           d.SKU(),
		Quantity:      d.Quantity(),
		Grade:         d.Grade().String(),
		Outcome:       string(d.Outcome()),
		PolicyVersion: d.PolicyVersion(),
		Supersedes:    d.Supersedes(),
		Reason:        d.Reason(),
		Staged:        d.Staged(),
		DecidedAt:     formatTime(d.DecidedAt()),
		Version:       snap.Version,
	}
}

// inspectionResultDTO is the wire shape of the inspect response
// (InspectionResult). The inspected-at instant and the reason codes are the
// ones the use case stamped the line with (the domain stores exactly the
// request's codes and decides at the same now), surfaced through the
// disposition's decidedAt and the echoed request.
type inspectionResultDTO struct {
	ReceiptID   string         `json:"receiptId"`
	LineNo      int32          `json:"lineNo"`
	SKU         string         `json:"sku"`
	Quantity    int64          `json:"quantity"`
	Grade       string         `json:"grade"`
	ReasonCodes []string       `json:"reasonCodes"`
	InspectedAt string         `json:"inspectedAt"`
	Disposition dispositionDTO `json:"disposition"`
}

// policyRuleDTO is one row of the policy table (PolicyRule).
type policyRuleDTO struct {
	Grade        string `json:"grade"`
	ProductClass string `json:"productClass"`
	Outcome      string `json:"outcome"`
}

// policyDTO is the wire shape of one policy version (DispositionPolicy).
type policyDTO struct {
	Version          int64           `json:"version"`
	ReturnWindowDays int32           `json:"returnWindowDays"`
	MaxUnitsPerLine  int64           `json:"maxUnitsPerLine"`
	VetoOutcome      string          `json:"vetoOutcome"`
	Rules            []policyRuleDTO `json:"rules"`
	DeclaredAt       string          `json:"declaredAt"`
}

// policyDTOFrom projects the read view (usecases.PolicyView of the
// aggregate).
func policyDTOFrom(p views.PolicyView) policyDTO {
	dto := policyDTO{
		Version:          p.Version,
		ReturnWindowDays: p.ReturnWindowDays,
		MaxUnitsPerLine:  p.MaxUnitsPerLine,
		VetoOutcome:      p.VetoOutcome,
		Rules:            make([]policyRuleDTO, 0, len(p.Rules)),
		DeclaredAt:       formatTime(p.DeclaredAt),
	}
	for _, r := range p.Rules {
		dto.Rules = append(dto.Rules, policyRuleDTO{
			Grade:        r.Grade.String(),
			ProductClass: r.ProductClass,
			Outcome:      string(r.Outcome),
		})
	}
	return dto
}
