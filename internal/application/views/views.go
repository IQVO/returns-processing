// Package views holds the application's read DTOs: the small wire-friendly
// shapes that cross the port boundaries. The ports package stays
// interfaces-only (arch rule); the domain aggregates never leak past the
// application layer.
//
// Two kinds live here:
//   - the local-copy DTOs (AllocatedLine, ManifestedUnit, ProductClass) the
//     event-fed read ports of ADR 0003 hand to the use cases, and
//   - the outbound read views (ReturnView, DispositionView, PolicyView) the
//     inbound adapters receive from the read use cases.
package views

import "time"

// AllocatedLine is one order line of the allocations local copy (ADR 0003),
// fed by order-management's LineAllocated facts.
type AllocatedLine struct {
	// SKU is the sku the allocated line carries; it is the authoritative sku
	// of a return request, not the caller's claim.
	SKU string
}

// ManifestedUnit is one shipped work unit of the package-manifest local copy
// (ADR 0003), fed by fulfillment-execution's PackageManifested facts. Its
// key is the WES work unit `{order_id}-line-{line_no}`.
type ManifestedUnit struct {
	// PackageID is the physical package the line shipped in.
	PackageID string
	// ManifestedAt is when the work unit was manifested.
	ManifestedAt time.Time
}

// ProductClass is one SKU's classification copy (ADR 0003), fed by
// product-master's ProductClassified facts. The raw tags carry the
// classification facts; the application derives the domain Classification
// with this grammar (unknown tags are ignored):
//
//	class:<name>   the policy-table product class, e.g. "class:Hazmat"
//	hazmat         the SKU needs hazmat handling
//	dot:<n>        the DOT hazard class rating, e.g. "dot:3" (0 = none)
//	force:<OUTCOME> a mandatory outcome that overrides the table, e.g.
//	               "force:DISPOSE" for a never-restock SKU
type ProductClass struct {
	Tags             []string
	TemperatureClass string
	Version          int64
}

// ReturnView is the read DTO for one RMA: the aggregate's state as
// wire-friendly scalars. The domain exposes no decided/received/closed
// instants, so the view carries exactly what the aggregate reports.
type ReturnView struct {
	ID            string
	OrderID       string
	LineNo        int32
	WorkUnit      string
	SKU           string
	Quantity      int64
	SiteID        string
	ReasonCode    string
	Status        string
	RejectionCode string
	WindowEndsAt  time.Time
	Version       int64
}

// DispositionView is the read DTO for one disposition decision.
type DispositionView struct {
	ID            string
	ReceiptID     string
	LineNo        int32
	SKU           string
	Quantity      int64
	Grade         string
	Outcome       string
	PolicyVersion int64
	Supersedes    string
	Reason        string
	SiteID        string
	RMAID         string
	DecidedAt     time.Time
	Version       int64
}

// PolicyView is the read DTO for one declared policy version. The domain
// aggregate exposes no rule-table accessor, so the view carries the policy's
// scalar bounds only — a redeclaration comes in through the use case input,
// which does carry the rules.
type PolicyView struct {
	Version          int64
	ReturnWindowDays int32
	MaxUnitsPerLine  int64
	VetoOutcome      string
	DeclaredAt       time.Time
}

// IdempotencyRecord is the stored answer of one Idempotency-Key: what the
// first request with that key answered, so a replay can return it verbatim.
// Body is the captured response body (with its headers, JSON-encoded by the
// HTTP middleware); Fingerprint identifies the request (method, path, body)
// so the same key with a different request is told apart.
type IdempotencyRecord struct {
	Key         string
	Fingerprint string
	Status      int
	ContentType string
	Body        []byte
	CreatedAt   time.Time
}
