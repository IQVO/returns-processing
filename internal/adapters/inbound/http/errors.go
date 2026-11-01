package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs
// (apis/openapi.yaml).
const problemBaseURI = "https://errors.returns-processing.warehouse-systems.dev/"

// Errors the adapter itself raises, before or besides the use cases.
var (
	// errMalformedRequest marks a body the adapter rejects (bad JSON, unknown
	// field, wrong type, trailing data, empty required body, a NUL byte) and
	// an invalid path or query parameter.
	errMalformedRequest = errors.New("request body is not valid JSON for this operation")
)

// problem is the fixed (status, slug, title) of one error category. detail,
// when set, replaces the error's own message (used for domain errors, whose
// text carries a package prefix and is never sent to a client as is).
type problem struct {
	status int
	slug   string
	title  string
	detail string
}

var internalProblem = problem{status: http.StatusInternalServerError, slug: "internal-error", title: "Internal server error"}

// Problems the middleware writes itself, before any use case runs.
var (
	idempotencyKeyRequired = problem{status: http.StatusBadRequest, slug: "idempotency-key-required", title: "Idempotency-Key header is required"}
	idempotencyKeyInvalid  = problem{status: http.StatusBadRequest, slug: "idempotency-key-invalid", title: "Idempotency-Key header is invalid"}
	// idempotencyKeyReused is 409 Conflict in this service's spec (the
	// Conflict response's idempotencyKeyReused example).
	idempotencyKeyReused = problem{status: http.StatusConflict, slug: "idempotency-key-reused", title: "Idempotency-Key was already used with a different request"}
	// notFoundProblem is the generic 404 of a path that names no resource
	// (a NUL byte in a path is 404 per .claude/rules/rest-api.md; the spec's
	// typed not-found slugs are used by the handlers below).
	notFoundProblem = problem{status: http.StatusNotFound, slug: "not-found", title: "Not found"}
	// dependencyUnavailable is the 503 of /readyz's dependency checks.
	dependencyUnavailable = problem{status: http.StatusServiceUnavailable, slug: "dependency-unavailable", title: "A runtime dependency is unavailable"}
)

// The typed not-found problems of the resources (apis/openapi.yaml, NotFound
// response examples). Which one applies depends on the route, so the handlers
// pick theirs instead of the catalogue doing errors.Is on the one shared
// ports.ErrNotFound sentinel.
var (
	rmaNotFound         = problem{http.StatusNotFound, "rma-not-found", "Return authorization not found", "return authorization not found"}
	receiptNotFound     = problem{http.StatusNotFound, "receipt-not-found", "Return receipt not found", "return receipt not found"}
	dispositionNotFound = problem{http.StatusNotFound, "disposition-not-found", "Disposition not found", "disposition not found"}
	policyNotFound      = problem{http.StatusNotFound, "policy-not-found", "No disposition policy declared", "no disposition policy version exists yet"}
)

var (
	malformedBody    = problem{http.StatusBadRequest, "malformed-request-body", "Malformed request body", ""}
	invalidQuantity  = problem{http.StatusBadRequest, "invalid-quantity", "Quantity is invalid", "quantity must be an integer of at least 1"}
	invalidLine      = problem{http.StatusBadRequest, "invalid-line", "Receipt line is invalid", "every line must be kind MATCHED with an rmaId, or kind UNEXPECTED"}
	invalidGrade     = problem{http.StatusBadRequest, "invalid-grade", "Grade is invalid", "grade must be one of A, B, C, SCRAP"}
	overrideReason   = problem{http.StatusBadRequest, "override-reason-required", "An override must say why", "reason must not be blank"}
	invalidPolicy    = problem{http.StatusBadRequest, "invalid-policy", "Disposition policy is invalid", "every grade needs a productClass \"*\" default rule and maxUnitsPerLine must be at least 1"}
	notAuthorizable  = problem{http.StatusConflict, "return-not-authorizable", "The order line is not authorizable", ""}
	openRMAExists    = problem{http.StatusConflict, "open-rma-exists", "An RMA is already open for this order line", "one open RMA per (orderRef, lineNo); close or receive the open one first"}
	rmaNotReceivable = problem{http.StatusConflict, "rma-not-receivable", "The RMA cannot receive goods", "only an AUTHORIZED RMA can be received"}
	lineInspected    = problem{http.StatusConflict, "line-already-inspected", "The receipt line is already inspected", "a return line is inspected exactly once"}
	inspectedExceeds = problem{http.StatusConflict, "inspected-exceeds-received", "The inspected quantity exceeds the received quantity", "inspected quantity must be at most the received quantity of the line"}
	invalidLimit     = problem{http.StatusBadRequest, "invalid-limit", "limit is invalid", "limit must be an integer between 1 and 500"}
	invalidCursor    = problem{http.StatusBadRequest, "invalid-cursor", "cursor is invalid", "cursor is not a cursor issued by this API"}
	concurrentMod    = problem{http.StatusConflict, "concurrent-modification", "The resource was modified by another request; re-fetch the latest version and retry", "concurrent modification"}
)

// problemCatalogue maps every typed error to its problem, in match order.
// The slugs are the catalogue of apis/openapi.yaml's response examples.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	// 400: the adapter's own request-shape errors.
	{errMalformedRequest, malformedBody},
	{shared.ErrQuantityBelowOne, invalidQuantity},
	{shared.ErrLineNoBelowOne, malformedBody},
	{shared.ErrBlankID, malformedBody},
	{shared.ErrNULInID, malformedBody},
	{returnauthorization.ErrBlankReasonCode, malformedBody},
	// 400: receipt-line shape.
	{returnreceipt.ErrLineKindMissing, invalidLine},
	{returnreceipt.ErrMatchedLineWithoutRMA, invalidLine},
	{returnreceipt.ErrUnexpectedLineWithRMA, invalidLine},
	{returnreceipt.ErrDuplicateLineNo, invalidLine},
	{returnreceipt.ErrNoLines, invalidLine},
	{returnreceipt.ErrInvalidGrade, invalidGrade},
	// 400: grade and override shape.
	{disposition.ErrInvalidGrade, invalidGrade},
	{disposition.ErrOverrideReasonRequired, overrideReason},
	// 400: policy-table shape.
	{disposition.ErrPolicyNotTotal, invalidPolicy},
	{disposition.ErrInvalidPolicy, invalidPolicy},
	{disposition.ErrDuplicateRule, invalidPolicy},
	{disposition.ErrInvalidVetoOutcome, invalidPolicy},
	// 409: authorization refusals (the detail carries the rejection code).
	{returnauthorization.ErrLineNotManifested, notAuthorizable},
	{returnauthorization.ErrWindowExpired, notAuthorizable},
	{returnauthorization.ErrQuantityExceedsPolicy, notAuthorizable},
	{usecases.ErrOrderLineNotAllocated, notAuthorizable},
	{returnauthorization.ErrOpenRMAExists, openRMAExists},
	// 409: terminal conflicts of the receipt and disposition flow.
	{returnauthorization.ErrNotAuthorized, rmaNotReceivable},
	{returnauthorization.ErrTerminal, rmaNotReceivable},
	{returnauthorization.ErrAlreadyDecided, rmaNotReceivable},
	{returnreceipt.ErrLineAlreadyInspected, lineInspected},
	{returnreceipt.ErrInspectedExceedsReceived, inspectedExceeds},
	{ports.ErrConcurrentModification, concurrentMod},
	// Backstop: a receipt whose addressed line does not exist is a miss of
	// the addressed resource (the inspect handler names the line).
	{returnreceipt.ErrLineNotFound, receiptNotFound},
}

// problemFor returns the problem of the first catalogue entry err matches,
// or the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}

// rejectionDetail is the 409 return-not-authorizable detail of an
// authorization refusal: "<CODE>: <explanation>", the rejection code the
// domain's RejectionCodeFor derives from the typed error (the spec's Conflict
// examples).
func rejectionDetail(err error) string {
	switch returnauthorization.RejectionCodeFor(err) {
	case returnauthorization.RejectLineNotManifested:
		return "LINE_NOT_MANIFESTED: no manifested work unit exists for this order line"
	case returnauthorization.RejectReturnWindowExpired:
		return "RETURN_WINDOW_EXPIRED: the return window ends before now"
	case returnauthorization.RejectQuantityExceedsMax:
		return "QUANTITY_EXCEEDS_POLICY: quantity must be at most maxUnitsPerLine"
	}
	// The one refusal with no domain rejection code: the order line is not
	// in the allocations local copy (fail-closed, ADR 0003).
	return "ORDER_LINE_NOT_ALLOCATED: the order line is not in the allocations copy"
}
