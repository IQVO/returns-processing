package main

import (
	"log/slog"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	inbounda2a "github.com/claudioed/returns-processing/internal/adapters/inbound/a2a"
	"github.com/claudioed/returns-processing/internal/adapters/inbound/requestlog"
)

// newAgent composes the A2A surface over the wired read use cases: the Agent
// Card advertising agentURL and the HTTP router around the JSON-RPC handler.
// The adapter never writes, so no EventPublisher, UnitOfWork or Clock is
// wired, and it calls no other context.
func newAgent(deps inbounda2a.Deps, agentURL, version string, logger *slog.Logger, serviceName string) http.Handler {
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: agentURL, Version: version})
	return newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)), card, logger, serviceName)
}

// newRouter wraps the A2A handler in the process's HTTP surface:
//
//   - GET /healthz  -> 200 {"status":"ok"} (open; liveness/readiness probes).
//   - GET /.well-known/agent-card.json -> the Agent Card (the discovery
//     contract, ADR 0008).
//   - /  and /a2a   -> the JSON-RPC endpoint. Both paths are served so the
//     canonical root and the fleet's <CTX>_A2A_ENDPOINT convention
//     (".../a2a", mirroring cmd/mcp's dual "/mcp" mount) both work.
//
// The middleware order is the fleet observability standard (ADR 0011),
// identical to the REST router: RequestID, otelchi (server span named after the
// route pattern), the request-duration histogram, RequestLogger, Recoverer.
// telemetry.Setup must have run before this call (otelchimetric binds the
// global MeterProvider at construction).
//
// Every path other than the three above still reaches the JSON-RPC handler
// ("/*"), as it did on the ServeMux this router replaced.
//
// No auth middleware is ever added here (TestNoAuthMiddlewareReintroduced):
// A2A is unauthenticated by the same fleet-wide decision as REST and MCP.
func newRouter(a2aHandler http.Handler, card *a2a.AgentCard, logger *slog.Logger, serviceName string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(otelchi.Middleware(serviceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(serviceName)))
	r.Use(requestlog.RequestLogger(logger))
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	r.Handle("/a2a", a2aHandler)
	r.Handle("/*", a2aHandler)
	return r
}
