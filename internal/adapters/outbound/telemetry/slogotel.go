package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Attribute keys for the ids that tie a log line back to a trace. These are
// the conventional flat names log backends (Loki, Elastic, the OTel
// Collector's own log pipeline) expect to correlate on.
const (
	TraceIDKey = "trace_id"
	SpanIDKey  = "span_id"
)

// traceHandler decorates another slog.Handler, stamping trace_id/span_id onto
// every record written with a context that carries a recording span. Records
// written without one (startup, shutdown, background work) pass through
// untouched, so the handler is safe to install unconditionally.
type traceHandler struct {
	inner slog.Handler
}

// WithTraceContext wraps inner so its records carry trace_id/span_id whenever
// a valid span context is in scope. Wrap the JSON handler with this once, at
// the composition root, and every logger derived from it correlates:
//
//	slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, opts)))
//
// Correlation only happens for the *Context variants of the slog API
// (InfoContext, ErrorContext, ...): slog.Info and friends pass a background
// context, which by definition has no span.
func WithTraceContext(inner slog.Handler) slog.Handler {
	return &traceHandler{inner: inner}
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record = record.Clone()
		record.AddAttrs(
			slog.String(TraceIDKey, sc.TraceID().String()),
			slog.String(SpanIDKey, sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, record)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{inner: h.inner.WithGroup(name)}
}
