package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

// unreachableEndpoint is a port nothing listens on. The whole point of the
// checks below is that this is indistinguishable, from the service's point of
// view, from a healthy Collector.
const unreachableEndpoint = "127.0.0.1:14317"

// setupDeadline is generous enough to be stable on a loaded CI runner while
// still failing loudly if Setup ever starts dialing synchronously — a
// blocking dial would sit there for the gRPC default connect timeout
// (20 seconds), an order of magnitude over this.
const setupDeadline = 3 * time.Second

func TestSetup_DoesNotBlockWithoutACollector(t *testing.T) {
	done := make(chan func(context.Context) error, 1)
	errs := make(chan error, 1)

	go func() {
		shutdown, err := Setup(context.Background(), "returns-processing-test", "test", unreachableEndpoint)
		if err != nil {
			errs <- err
			return
		}
		done <- shutdown
	}()

	var shutdown func(context.Context) error
	select {
	case err := <-errs:
		t.Fatalf("Setup returned an error with no collector listening: %v", err)
	case shutdown = <-done:
	case <-time.After(setupDeadline):
		t.Fatalf("Setup did not return within %s: the exporter is dialing synchronously", setupDeadline)
	}

	// A tracer from the installed provider must produce real, recording
	// spans — an unreachable collector costs export, not instrumentation.
	_, span := otel.Tracer("test").Start(context.Background(), "probe")
	if !span.SpanContext().IsValid() {
		t.Error("span context is invalid: Setup did not install a real TracerProvider")
	}
	if !span.IsRecording() {
		t.Error("span is not recording: Setup did not install a real TracerProvider")
	}
	span.End()

	if otel.GetTextMapPropagator() == nil {
		t.Error("Setup did not install a text map propagator")
	}

	// Shutdown flushes through the same dead endpoint. It is allowed to
	// report the export failure; what it must not do is hang past the
	// context deadline it was handed.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), setupDeadline)
	defer cancel()

	shutdownDone := make(chan struct{})
	go func() {
		_ = shutdown(shutdownCtx)
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
	case <-time.After(setupDeadline * 2):
		t.Fatal("shutdown did not honour its context deadline")
	}
}

// An empty endpoint is not "telemetry off": Setup falls back to
// DefaultEndpoint (the Collector's standard OTLP/gRPC port on localhost) and
// still installs real providers, exactly like the fleet reference.
func TestSetup_EmptyEndpointFallsBackToTheDefaultAndStillInstrumentsSpans(t *testing.T) {
	t.Setenv(endpointEnvVar, "")

	if DefaultEndpoint != "localhost:4317" {
		t.Fatalf("DefaultEndpoint = %q, want localhost:4317", DefaultEndpoint)
	}

	shutdown, err := Setup(context.Background(), "returns-processing-test", "", "")
	if err != nil {
		t.Fatalf("Setup with an empty endpoint returned an error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), setupDeadline)
		defer cancel()
		_ = shutdown(ctx)
	})

	_, span := otel.Tracer("test").Start(context.Background(), "probe")
	defer span.End()
	if !span.IsRecording() {
		t.Error("span is not recording: an empty endpoint must not turn Setup into a no-op")
	}
}

func TestNewResource_CarriesServiceIdentity(t *testing.T) {
	t.Setenv("ENVIRONMENT", "staging")

	res, err := newResource("returns-processing", "1.2.3")
	if err != nil {
		t.Fatalf("newResource returned error: %v", err)
	}

	want := map[string]string{
		"service.name":                "returns-processing",
		"service.version":             "1.2.3",
		"deployment.environment.name": "staging",
	}

	got := map[string]string{}
	for _, attr := range res.Attributes() {
		got[string(attr.Key)] = attr.Value.AsString()
	}

	for key, value := range want {
		if got[key] != value {
			t.Errorf("resource attribute %s = %q, want %q", key, got[key], value)
		}
	}
}

func TestEnvironment_DefaultsToLocal(t *testing.T) {
	t.Setenv("ENVIRONMENT", "")

	if got := environment(); got != DefaultEnvironment {
		t.Errorf("environment() = %q, want %q", got, DefaultEnvironment)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{
			name:     "a bare host:port becomes a plaintext URL",
			endpoint: "otel-collector:4317",
			want:     "http://otel-collector:4317",
		},
		{
			name:     "empty falls back to the collector's default gRPC port",
			endpoint: "",
			want:     "http://localhost:4317",
		},
		{
			name:     "an explicit URL is left alone so TLS is honoured",
			endpoint: "https://otel-collector.observability.svc:4317",
			want:     "https://otel-collector.observability.svc:4317",
		},
		{
			name:     "an explicit plaintext URL is left alone",
			endpoint: "http://localhost:4317",
			want:     "http://localhost:4317",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Isolated from the ambient environment so the in-place rewrite
			// below cannot leak between tests.
			t.Setenv(endpointEnvVar, "")

			if got := normalizeEndpoint(tt.endpoint); got != tt.want {
				t.Errorf("normalizeEndpoint(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}
}

// The exporters read OTEL_EXPORTER_OTLP_ENDPOINT themselves and reject a
// value with no scheme; rewriting it keeps a valid bare host:port from
// producing a spurious parse error at every startup.
func TestNormalizeEndpoint_RewritesTheBareEnvVarInPlace(t *testing.T) {
	t.Setenv(endpointEnvVar, "otel-collector:4317")

	normalizeEndpoint("otel-collector:4317")

	if got := os.Getenv(endpointEnvVar); got != "http://otel-collector:4317" {
		t.Errorf("%s = %q, want the normalized URL", endpointEnvVar, got)
	}
}

func TestNormalizeEndpoint_LeavesAURLEnvVarAlone(t *testing.T) {
	t.Setenv(endpointEnvVar, "https://otel-collector:4317")

	normalizeEndpoint("https://otel-collector:4317")

	if got := os.Getenv(endpointEnvVar); got != "https://otel-collector:4317" {
		t.Errorf("%s = %q, want it untouched", endpointEnvVar, got)
	}
}
