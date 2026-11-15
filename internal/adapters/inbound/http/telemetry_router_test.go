package http_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

const requestDurationMetric = "http.server.request.duration"

// installManualReader makes a ManualReader-backed MeterProvider the global one
// for the duration of the test. It must run BEFORE the router under test is
// built: otelchimetric binds otel.GetMeterProvider() at construction.
func installManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })
	return reader
}

// durationPoints returns the data points of http.server.request.duration.
func durationPoints(t *testing.T, reader sdkmetric.Reader) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != requestDurationMetric {
				continue
			}
			if m.Unit != "s" {
				t.Errorf("%s unit = %q, want s", requestDurationMetric, m.Unit)
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s is a %T, want a float64 Histogram", requestDurationMetric, m.Data)
			}
			return h.DataPoints
		}
	}
	return nil
}

func attrString(dp metricdata.HistogramDataPoint[float64], key string) string {
	v, _ := dp.Attributes.Value(attribute.Key(key))
	return v.AsString()
}

func newRESTRouter(logger *slog.Logger) http.Handler {
	rmas := memory.NewRMAs()
	return inboundhttp.NewRouter(&inboundhttp.Server{
		GetReturn:   &usecases.GetReturn{RMAs: rmas},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: rmas},
		Logger:      logger,
		ServiceName: "returns-processing-test",
	})
}

// Fleet observability standard, Tier 1: a request through the REST router
// produces http.server.request.duration, and its route label is the chi
// pattern, never the raw path (an rma id would mint one series per return).
func TestRouter_RequestProducesTheDurationHistogramLabelledWithTheRoutePattern(t *testing.T) {
	reader := installManualReader(t)
	router := newRESTRouter(nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/returns/rma-CARDINALITY", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /returns/rma-CARDINALITY = %d, want 404 (the rma does not exist)", rec.Code)
	}

	points := durationPoints(t, reader)
	if len(points) != 1 {
		t.Fatalf("%s data points = %d, want 1", requestDurationMetric, len(points))
	}
	if got := attrString(points[0], "http.route"); got != "/returns/{rmaId}" {
		t.Errorf("http.route = %q, want the route pattern /returns/{rmaId}", got)
	}
	if points[0].Count != 1 {
		t.Errorf("recorded %d requests, want 1", points[0].Count)
	}
}

func TestRouter_HealthzIsUnchangedAndStillMeasured(t *testing.T) {
	reader := installManualReader(t)
	router := newRESTRouter(nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", rec.Code, rec.Body.String())
	}
	if len(durationPoints(t, reader)) != 1 {
		t.Errorf("/healthz produced no %s", requestDurationMetric)
	}
}

func TestReportsRouter_RequestProducesTheDurationHistogram(t *testing.T) {
	reader := installManualReader(t)
	router := inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{ServiceName: "returns-reports-test"})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	points := durationPoints(t, reader)
	if len(points) != 1 {
		t.Fatalf("%s not emitted by the reports router (%d points)", requestDurationMetric, len(points))
	}
	if got := attrString(points[0], "http.route"); got != "/healthz" {
		t.Errorf("http.route = %q, want /healthz", got)
	}
}

// spanCapturingHandler stands in for the trace-correlating slog handler the
// composition roots install. That handler lives in the outbound telemetry
// adapter, which this inbound adapter must not import, so what is asserted
// here is the part this layer owns: RequestLogger hands its records a context
// carrying the request's span. Turning that context into trace_id/span_id is
// tested where the handler lives.
type spanCapturingHandler struct {
	slog.Handler
	seen trace.SpanContext
}

func (h *spanCapturingHandler) Handle(ctx context.Context, record slog.Record) error {
	h.seen = trace.SpanContextFromContext(ctx)
	return h.Handler.Handle(ctx, record)
}

func TestRouter_ServerSpanIsNamedAfterTheRoutePatternAndTheRequestLogIsWrittenUnderIt(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	logHandler := &spanCapturingHandler{Handler: slog.NewJSONHandler(&bytes.Buffer{}, nil)}
	router := newRESTRouter(slog.New(logHandler))

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/returns/rma-1", nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", spans[0].SpanKind())
	}
	if name := spans[0].Name(); name != "/returns/{rmaId}" {
		t.Errorf("span name = %q, want the route pattern /returns/{rmaId}", name)
	}
	want := spans[0].SpanContext()
	if !logHandler.seen.IsValid() {
		t.Fatal("the request log was written with a context carrying no span: middleware order or the *Context logger variants are wrong")
	}
	if logHandler.seen.TraceID() != want.TraceID() || logHandler.seen.SpanID() != want.SpanID() {
		t.Errorf("log span = %s/%s, want %s/%s", logHandler.seen.TraceID(), logHandler.seen.SpanID(), want.TraceID(), want.SpanID())
	}
}
