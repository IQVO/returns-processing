package main

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/requestlog"
)

// newRouter wraps the MCP handler in a small chi router so the binary is
// deployable behind Kubernetes probes:
//
//   - GET /healthz  -> 200 {"status":"ok"} (open; liveness/readiness probes).
//   - /  and /mcp   -> the MCP Streamable HTTP handler. Both paths are served
//     so warehouse-ops-agent's *_MCP_ENDPOINT convention (".../mcp") and a
//     root-mounted endpoint both work.
//
// The middleware order is the fleet observability standard (ADR 0011),
// identical to the REST router: RequestID, otelchi (server span named after the
// route pattern), the request-duration histogram, RequestLogger, Recoverer.
// telemetry.Setup must have run before this call (otelchimetric binds the
// global MeterProvider at construction).
//
// No auth middleware is ever added here (TestNoAuthMiddlewareReintroduced).
func newRouter(mcpHandler http.Handler, logger *slog.Logger, serviceName string) http.Handler {
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
	r.Mount("/mcp", mcpHandler)
	r.Mount("/", mcpHandler)
	return r
}
