package a2a

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Every skill run is one OTel span "a2a.skill <id>" carrying the a2a.skill
// attribute (mirroring the MCP adapter's mcp.tool spans); a problem answer
// marks the span as an error. Not parallel: it swaps the global provider.
func TestEachSkillRunIsOneSpanWithTheSkillAttribute(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	f := newFixture()
	f.declarePolicy(t)
	w := newWire(t, f.deps())
	w.skill(t, skillDispositionPolicy, map[string]any{})
	w.skill(t, skillReturnStatus, map[string]any{"rmaId": "rma-unknown"})

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want one per skill run: %v", len(spans), spans)
	}
	for i, want := range []struct {
		skill string
		code  codes.Code
	}{{skillDispositionPolicy, codes.Unset}, {skillReturnStatus, codes.Error}} {
		got := spans[i]
		if got.Name != "a2a.skill "+want.skill {
			t.Errorf("span %d name = %q, want %q", i, got.Name, "a2a.skill "+want.skill)
		}
		if !hasAttr(got.Attributes, "a2a.skill", want.skill) {
			t.Errorf("span %d attributes = %v, want a2a.skill=%s", i, got.Attributes, want.skill)
		}
		if got.Status.Code != want.code {
			t.Errorf("span %d status = %v, want %v", i, got.Status.Code, want.code)
		}
	}
}

func hasAttr(attrs []attribute.KeyValue, key, value string) bool {
	for _, a := range attrs {
		if string(a.Key) == key && a.Value.AsString() == value {
			return true
		}
	}
	return false
}
