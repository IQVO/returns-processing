package a2a

import (
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/returns-processing/internal/application/views"
)

// problemTypeBase namespaces this context's problem types. It is the SAME
// namespace as the REST adapter's RFC 7807 types (problemBaseURI in
// internal/adapters/inbound/http/errors.go), so a delegating agent sees the
// identifiers a REST caller would for the outcomes both surfaces share.
const problemTypeBase = "https://errors.returns-processing.warehouse-systems.dev/"

// problem is the RFC 7807-shaped rejection an agent client receives instead of
// a result: domain rejections and request-shape failures are business
// answers, not transport errors, so they travel in the message envelope.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

// summary renders the one-line form used for the reply's text part.
func (p *problem) summary() string {
	return fmt.Sprintf("%s (%d): %s", p.Title, p.Status, p.Detail)
}

func problemMalformedRequest(detail string) *problem {
	return &problem{Type: problemTypeBase + "malformed-skill-request", Title: "Malformed skill request", Detail: detail, Status: 400}
}

func problemUnknownSkill(skill string) *problem {
	return &problem{Type: problemTypeBase + "unknown-skill", Title: "Unknown skill", Detail: fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill), Status: 400}
}

func problemInvalidArgument(detail string) *problem {
	return &problem{Type: problemTypeBase + "invalid-argument", Title: "Invalid skill argument", Detail: detail, Status: 400}
}

// REST-aligned problems of the reads: the slugs, titles, details and statuses
// are the REST adapter's rmaNotFound / policyNotFound / internalProblem.
func problemRMANotFound() *problem {
	return &problem{Type: problemTypeBase + "rma-not-found", Title: "Return authorization not found", Detail: "return authorization not found", Status: 404}
}

func problemPolicyNotFound(detail string) *problem {
	return &problem{Type: problemTypeBase + "policy-not-found", Title: "No disposition policy declared", Detail: detail, Status: 404}
}

func problemInternal() *problem {
	return &problem{Type: problemTypeBase + "internal-error", Title: "Internal server error", Detail: "an unexpected error occurred; see server logs", Status: 500}
}

// envelope is the structured payload of every skill answer: exactly one of
// Data (a skill DTO) or Problem (why there is no result), plus the skill id
// the answer belongs to.
type envelope struct {
	Skill   string    `json:"skill"`
	Data    *skillDTO `json:"data,omitempty"`
	Problem *problem  `json:"problem,omitempty"`
}

// skillDTO is the union of the skill result DTOs. A2A parts are
// machine-readable JSON; each skill fills its own field, keeping the envelope
// flat and versioned as one contract.
type skillDTO struct {
	Return  *returnDTO  `json:"return,omitempty"`
	Returns *returnsDTO `json:"returns,omitempty"`
	Policy  *policyDTO  `json:"policy,omitempty"`
	summary string
}

// Summary is the human-readable one-liner for the reply's text part.
func (s skillDTO) Summary() string { return s.summary }

// returnDTO is the message-boundary representation of one RMA: the field
// names of the REST wire (rmaId, orderId, lineNo, ...) plus the work unit the
// MCP tool carries. Domain types never cross the boundary.
type returnDTO struct {
	RMAID         string `json:"rmaId"`
	OrderID       string `json:"orderId"`
	LineNo        int32  `json:"lineNo"`
	WorkUnit      string `json:"workUnit,omitempty"`
	SKU           string `json:"sku"`
	Quantity      int64  `json:"quantity"`
	SiteID        string `json:"siteId"`
	ReasonCode    string `json:"reasonCode,omitempty"`
	RejectionCode string `json:"rejectionCode,omitempty"`
	Status        string `json:"status"`
	WindowEndsAt  string `json:"windowEndsAt,omitempty"`
	Version       int64  `json:"version"`
}

// returnsDTO is the order view of return_status: the order asked about and
// its returns (always present; [] when the order has none).
type returnsDTO struct {
	OrderID string      `json:"orderId"`
	Items   []returnDTO `json:"items"`
}

type policyRuleDTO struct {
	Grade        string `json:"grade"`
	ProductClass string `json:"productClass"`
	Outcome      string `json:"outcome"`
}

// policyDTO is the message-boundary representation of one declared policy
// version.
type policyDTO struct {
	Version          int64           `json:"version"`
	ReturnWindowDays int32           `json:"returnWindowDays"`
	MaxUnitsPerLine  int64           `json:"maxUnitsPerLine"`
	VetoOutcome      string          `json:"vetoOutcome"`
	Rules            []policyRuleDTO `json:"rules"`
	DeclaredAt       string          `json:"declaredAt"`
}

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toReturnDTO(v views.ReturnView) returnDTO {
	return returnDTO{
		RMAID:         v.ID,
		OrderID:       v.OrderID,
		LineNo:        v.LineNo,
		WorkUnit:      v.WorkUnit,
		SKU:           v.SKU,
		Quantity:      v.Quantity,
		SiteID:        v.SiteID,
		ReasonCode:    v.ReasonCode,
		RejectionCode: v.RejectionCode,
		Status:        v.Status,
		WindowEndsAt:  instant(v.WindowEndsAt),
		Version:       v.Version,
	}
}

func toPolicyDTO(p views.PolicyView) policyDTO {
	out := policyDTO{
		Version:          p.Version,
		ReturnWindowDays: p.ReturnWindowDays,
		MaxUnitsPerLine:  p.MaxUnitsPerLine,
		VetoOutcome:      p.VetoOutcome,
		Rules:            make([]policyRuleDTO, 0, len(p.Rules)),
		DeclaredAt:       instant(p.DeclaredAt),
	}
	for _, r := range p.Rules {
		out.Rules = append(out.Rules, policyRuleDTO{Grade: r.Grade.String(), ProductClass: r.ProductClass, Outcome: string(r.Outcome)})
	}
	return out
}

// summarizeReturn is the one-line text form of one return.
func summarizeReturn(r returnDTO) string {
	s := fmt.Sprintf("%s: %s, order %s line %d, %d x %s", r.RMAID, r.Status, r.OrderID, r.LineNo, r.Quantity, r.SKU)
	if r.RejectionCode != "" {
		s += ", rejected: " + r.RejectionCode
	}
	return s
}

// summarizeReturns is the one-line text form of an order's returns.
func summarizeReturns(list returnsDTO) string {
	if len(list.Items) == 0 {
		return fmt.Sprintf("order %s has no returns", list.OrderID)
	}
	ids := make([]string, 0, len(list.Items))
	for _, r := range list.Items {
		ids = append(ids, r.RMAID+" ("+r.Status+")")
	}
	return fmt.Sprintf("order %s has %d returns: %s", list.OrderID, len(list.Items), strings.Join(ids, ", "))
}

// summarizePolicy is the one-line text form of a policy version.
func summarizePolicy(p policyDTO) string {
	return fmt.Sprintf("disposition policy v%d: %d-day return window, at most %d units per line, %d rules, veto %s",
		p.Version, p.ReturnWindowDays, p.MaxUnitsPerLine, len(p.Rules), p.VetoOutcome)
}
