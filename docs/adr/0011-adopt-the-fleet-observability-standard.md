# ADR 0011: Adopt the fleet observability standard

## Status

Accepted (2026-11-14). Supersedes in part [ADR 0009](0009-adopt-the-fleet-rest-api-guideline.md), whose
"spec drift removed" bullet left the service with no runtime metrics at all.

## Context

Until this ADR the service exported nothing but logs and two probes: no OpenTelemetry setup, no
HTTP request metrics, no OTLP export, log lines that could not be joined to a trace, and no business
counter. ADR 0009 had removed `GET /metrics` from `apis/openapi.yaml` because no handler served it;
that was a contract-drift fix, not a decision against observability, and it left the executive deck's
top ask for the repo open. The rest of the fleet reports through one standard (the Tier 1 / Tier 2
convention of inventory-storage's ADR 0016 and node-topology's ADR 0010): OTLP/gRPC push to the
Collector, the same router middleware, the same naming rules for business counters.

## Decision

returns-processing adopts the standard as the fleet reference implements it. The telemetry package
(`Setup`, `WithTraceContext`) is copied from node-topology, the fleet's multi-binary reference,
rather than mixed from two repos.

**Tier 1, identical everywhere.**

- `internal/adapters/outbound/telemetry.Setup` builds a TracerProvider and a MeterProvider that push
  over OTLP/gRPC, installs the W3C propagator and starts Go runtime metrics. An empty endpoint falls
  back to `localhost:4317`; export is lazy and non-blocking, so a missing Collector costs telemetry,
  never availability.
- Every long-running binary has its own `cmd/<binary>/telemetry.go` (`OTEL_SERVICE_NAME`,
  `OTEL_EXPORTER_OTLP_ENDPOINT`, `SERVICE_VERSION`; `ENVIRONMENT` is read by `Setup`): `cmd/api`,
  `cmd/mcp`, `cmd/a2a`, `cmd/returns-projector` and `cmd/returns-reports`. The projector serves only
  probes on an admin port, so it gets `Setup` and trace-correlated logs, not the HTTP middleware.
- Middleware order on every chi router: `RequestID`, `otelchi.Middleware` (server span named after
  the route pattern), `otelchimetric.NewServerRequestDuration`, `RequestLogger`, then the recoverer
  (REST and reports keep their own `recoverPanic`, which answers the 500 problem document; REST also
  keeps its NUL guard). `/healthz` and `/readyz` are unchanged.
- `RequestLogger` lives in its own package, `internal/adapters/inbound/requestlog`, because the
  read-only MCP and A2A binaries may not import the REST adapter.
- `cmd/a2a` moves from a `net/http.ServeMux` to a chi router to get the same middleware. Its
  catch-all keeps working: every path other than `/healthz`, the Agent Card and `/a2a` still reaches
  the JSON-RPC handler (`/*`).
- The slog handler of every binary is wrapped with `telemetry.WithTraceContext`, so records written
  with a span-carrying context carry `trace_id` and `span_id`.

**Tier 2, one business counter.** `returns.authorizations.decided` (unit `{authorization}`)
counts the decisions of `AuthorizeReturn`, with attributes `outcome` (`authorized` | `rejected`) and,
on a rejection, `reason`: the problem slug the REST adapter answers with (`return-not-authorizable`,
`open-rma-exists`, `concurrent-modification`, `invalid-quantity`, `malformed-request-body`,
`internal-error`). `AuthorizeReturn` is the primary command of the context: it is the only way an RMA
comes to exist, every later aggregate (receipt, disposition) hangs off it, and its refusals are the
business signal operators ask about. The use case reports through `ports.AuthorizationMetrics`
(nil means not counted); the implementation lives in `internal/adapters/outbound/telemetry`; nothing
in `internal/domain` knows it. The application layer cannot import the REST adapter, so the use case
owns the error-to-reason table and a test in the REST adapter pins it to the handler's slugs.

**Chart.** `otel.{enabled,endpoint,serviceName}`, `serviceVersion` (empty = image tag, then the chart
`appVersion`) and `environment` render `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`,
`SERVICE_VERSION` and `ENVIRONMENT` in every Deployment that runs a binary; `extraEnv` refuses those
names.

**Trace context across Kafka: not done.** The `outbox` table has no `headers` column (migration
`0001_returns-processing.up.sql`), so stamping `traceparent`/`tracestate` into the row would need a
migration. This ADR adds none: published events still carry only the CloudEvents `content-type`
header and a consumer's span starts a new trace. Adding the column and the propagation is a separate,
migration-bearing decision.

## Consequences

- `/metrics` stays undeclared: metrics are pushed, not scraped, so ADR 0009's removal of the route
  stands; what it implied ("no runtime metrics") no longer holds.
- No REST, MCP, A2A or CloudEvents contract changes; no migration; no domain change.
- The OTLP/gRPC dependencies are new direct dependencies; `govulncheck` guards them in CI.
- Dashboards and alerts are not part of this change.
