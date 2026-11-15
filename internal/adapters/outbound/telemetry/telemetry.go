// Package telemetry is the outbound OpenTelemetry adapter. It owns the
// process-wide TracerProvider and MeterProvider, exports both over OTLP/gRPC
// to a Collector, and supplies the slog handler that correlates log records
// with the span that is active when they are written.
//
// It sits in the same tier as the postgres/kafka/events adapters: nothing in
// internal/domain or internal/application imports it, and the composition
// root (cmd/*) is what wires it up.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

const (
	// DefaultEndpoint is the OTel Collector's standard OTLP/gRPC receiver
	// port. Overridden by OTEL_EXPORTER_OTLP_ENDPOINT.
	DefaultEndpoint = "localhost:4317"

	// DefaultServiceVersion is used when SERVICE_VERSION is unset.
	DefaultServiceVersion = "dev"

	// DefaultEnvironment is used when ENVIRONMENT is unset.
	DefaultEnvironment = "local"

	// metricInterval is how often the PeriodicReader pushes to the Collector.
	metricInterval = 15 * time.Second

	// endpointEnvVar is the OTLP spec's endpoint variable, which the
	// exporters read directly in addition to the options passed here.
	endpointEnvVar = "OTEL_EXPORTER_OTLP_ENDPOINT"
)

// Setup builds a TracerProvider and a MeterProvider that export over OTLP/gRPC
// to otlpEndpoint, installs them as the global providers along with the W3C
// trace-context propagator, starts Go runtime metrics collection, and returns
// a single func that flushes and closes both providers.
//
// otlpEndpoint accepts either a bare host:port ("otel-collector:4317") or a
// full URL ("http://otel-collector:4317"); empty falls back to
// DefaultEndpoint. deployment.environment.name comes from the ENVIRONMENT env
// var (default DefaultEnvironment) because it is deployment-time config, not
// something the composition root decides.
//
// Export is deliberately non-blocking: the gRPC exporters connect lazily and
// no blocking dial option is set, so a Collector that is down or absent
// degrades to "telemetry silently dropped", never to a service that refuses
// to start or requests that hang.
func Setup(ctx context.Context, serviceName, serviceVersion, otlpEndpoint string) (func(context.Context) error, error) {
	if serviceVersion == "" {
		serviceVersion = DefaultServiceVersion
	}

	// The SDK's own diagnostics go through logr; bridging it onto the
	// configured slog handler keeps slog the only thing writing to stdout,
	// so every line stays JSON and honours LOG_LEVEL.
	otel.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	endpoint := normalizeEndpoint(otlpEndpoint)

	res, err := newResource(serviceName, serviceVersion)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExporter),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(endpoint))
	if err != nil {
		// The tracer provider is already live at this point; tear it down
		// rather than leaking its batch processor goroutine.
		return nil, errors.Join(err, tracerProvider.Shutdown(ctx))
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(metricInterval))),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Exporter failures (the "no Collector listening" case) are expected in
	// local runs; log them at Debug so they are available when hunting a
	// pipeline problem without spamming a normal info-level log stream.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Debug("opentelemetry error", "error", err)
	}))

	// Goroutines, GC pauses, heap — the OTel-native replacement for
	// hand-rolled runtime stats.
	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return nil, errors.Join(err, meterProvider.Shutdown(ctx), tracerProvider.Shutdown(ctx))
	}

	return func(shutdownCtx context.Context) error {
		return errors.Join(
			tracerProvider.Shutdown(shutdownCtx),
			meterProvider.Shutdown(shutdownCtx),
		)
	}, nil
}

// newResource describes *this process* to the Collector. resource.Default()
// contributes telemetry.sdk.* and the OTEL_RESOURCE_ATTRIBUTES env var; the
// attributes below identify the service itself.
func newResource(serviceName, serviceVersion string) (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			semconv.DeploymentEnvironmentNameKey.String(environment()),
		),
	)
}

func environment() string {
	if v := os.Getenv("ENVIRONMENT"); v != "" {
		return v
	}
	return DefaultEnvironment
}

// normalizeEndpoint turns whatever form the endpoint was configured in into
// the URL the OTLP spec calls for. A bare host:port becomes plaintext http://,
// which is what a Collector's gRPC receiver speaks by default and matches the
// documented "localhost:4317".
//
// It also rewrites OTEL_EXPORTER_OTLP_ENDPOINT in place when that is where the
// bare form came from. The exporters read that variable themselves, before any
// option we pass is applied, and log a parse error for a value with no scheme
// — harmless, since our explicit option wins, but it would be an ERROR line at
// every startup for a perfectly valid configuration.
func normalizeEndpoint(endpoint string) string {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if strings.Contains(endpoint, "://") {
		return endpoint
	}

	normalized := "http://" + endpoint
	if raw, ok := os.LookupEnv(endpointEnvVar); ok && !strings.Contains(raw, "://") {
		_ = os.Setenv(endpointEnvVar, normalized) // only fails on an invalid name, which is a constant here.
	}

	return normalized
}
