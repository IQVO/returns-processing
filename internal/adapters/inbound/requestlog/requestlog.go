// Package requestlog is the access-log middleware shared by every chi router of
// this service (the REST adapter, cmd/mcp and cmd/a2a). It lives in its own package
// because the read-only MCP and A2A binaries may not import the REST adapter
// (the fitness tests under internal/architecture) yet must log requests the
// same way.
package requestlog

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// unmatchedRoute names the route of a request no pattern matched, so the log
// never carries a raw, attacker-chosen path.
const unmatchedRoute = "unmatched"

// quietPaths are probe endpoints hit every few seconds; logging them would
// drown the access log.
var quietPaths = map[string]bool{"/healthz": true, "/readyz": true}

// RequestLogger returns chi middleware that logs one line per request with the
// method, the matched route pattern (never the raw path: it holds ids), the
// status, duration_ms, the response bytes and the chi request id. A 5xx is
// logged at ERROR, a 4xx at WARN, everything else at INFO. /healthz and
// /readyz are skipped.
//
// Mount it AFTER otelchi so the request context already carries the server
// span: the *Context logger variants hand that context to the slog handler,
// which stamps trace_id and span_id on the record.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if quietPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			logger.Log(r.Context(), levelFor(status), "http request",
				"method", r.Method,
				"route", routePattern(r),
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", ww.BytesWritten(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

func levelFor(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// routePattern is the chi pattern the request matched ("/returns/{rmaId}"),
// resolved after the handler ran.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return unmatchedRoute
}
