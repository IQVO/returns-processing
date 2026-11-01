// Package http is the REST inbound adapter: every route of apis/openapi.yaml
// (.claude/rules/rest-api.md) on a chi router, RFC 7807 problem documents for
// every error, the Idempotency-Key middleware of the writes, the NUL-byte
// guard and the liveness/readiness endpoints. It has no auth middleware
// (fleet decision 2026-09-11;
// internal/architecture's TestNoAuthMiddlewareReintroduced).
package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// Server holds the use cases the handlers call.
type Server struct {
	AuthorizeReturn *usecases.AuthorizeReturn
	GetReturn       *usecases.GetReturn
	ListByOrder     *usecases.ListReturnsByOrder

	OpenReturnReceipt *usecases.OpenReturnReceipt
	InspectReturnLine *usecases.InspectReturnLine

	OverrideDisposition      *usecases.OverrideDisposition
	DeclareDispositionPolicy *usecases.DeclareDispositionPolicy
	GetDispositionPolicy     *usecases.GetDispositionPolicy

	// Idempotency wraps the POSTs and the PUT (the fleet Idempotency-Key
	// middleware, Idempotency.Middleware, built by cmd/ over the Postgres
	// adapters); nil leaves the routes unwrapped (handler unit tests only:
	// cmd/ always wires it).
	Idempotency func(http.Handler) http.Handler
	// Readiness backs GET /readyz; nil means always ready.
	Readiness *Readiness
}

// NewRouter builds the chi router for every endpoint of apis/openapi.yaml
// (.claude/rules/rest-api.md). rejectNUL wraps the whole tree OUTSIDE the
// idempotency group, so a NUL request is refused before idempotency records
// anything.
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverPanic)
	r.Use(rejectNUL)

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	r.Get("/returns", s.handleListReturnsByOrder)
	r.Get("/returns/{rmaId}", s.handleGetReturn)

	r.Get("/disposition-policies/current", s.handleGetCurrentPolicy)

	r.Group(func(r chi.Router) {
		if s.Idempotency != nil {
			r.Use(s.Idempotency)
		}
		r.Post("/returns", s.handleAuthorizeReturn)
		r.Post("/returns/{rmaId}/receipts", s.handleOpenReturnReceipt)
		r.Post("/receipts/{receiptId}/lines/{lineNo}/inspections", s.handleInspectReturnLine)
		r.Post("/dispositions/{dispositionId}/overrides", s.handleOverrideDisposition)
		r.Put("/disposition-policies/{version}", s.handleDeclarePolicy)
	})
	return r
}
