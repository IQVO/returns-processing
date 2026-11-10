# CLAUDE.md — returns-processing

`returns-processing` is a Supporting bounded context (`wms` tier) of the
`warehouse-systems` fleet (GitHub org `IQVO`, Go module
`github.com/claudioed/returns-processing`). It owns the reverse flow: it
authorizes returns (RMAs) against shipped order lines, receives and grades the
returned goods, decides each line's disposition from a versioned
`DispositionPolicy`, and hands restockable units to inventory-storage through
the staged-receipt seam. It owns the `ReturnAuthorization`, `ReturnReceipt`
and `Disposition` aggregates. It owns no stock (inventory-storage), no order
(order-management) and no exception case (exception-management). It ships five
binaries: `cmd/api` (REST + outbox relay + the three local-copy consumers),
`cmd/mcp` (read-only MCP), `cmd/a2a` (read-only A2A agent),
`cmd/returns-projector` and `cmd/returns-reports` (analytics read side), plus
the `web/` console remote. Agent authority: every MCP tool and A2A skill
declares a level (L1 read today; ADR 0010).

## Hard rules (each one cost an incident or is fitness-tested)

1. **Hexagonal dependency rule.** `internal/domain` depends on nothing internal
   but domain; `internal/application` only on domain and application; inbound
   and outbound adapters never depend on each other; only `cmd/` wires layers.
   No JSON tags, SQL or HTTP types in the domain. `make arch-test` enforces it
   (`internal/architecture/architecture_test.go`).
2. **CloudEvents 1.0 is MANDATORY** on every Kafka message produced or
   consumed (structured mode, no flat envelope, no dual-write, no envelope
   toggle; `TestNoEventEnvelopeToggleOrFlatEnvelope`, `TestCloudEventsOnly`).
   Build/decode ONLY through `internal/adapters/kafka/cloudevents/`; `type` is
   `com.warehouse.wms.returns-processing.<entity>.<EventName>`; a breaking
   payload change is a new `.v2` type, never a mutation. Catalogue:
   `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`; payloads:
   `apis/asyncapi.yaml`; rules: `.claude/rules/integration-events.md`.
3. **Events leave through the transactional outbox only** (ADR 0005): the
   aggregate save and the encoded CloudEvents commit in one unit of work; the
   relay in `cmd/api` publishes to `warehouse.returns-processing.events` and
   `warehouse.returns-processing.analytics`. Never write to Kafka from a
   handler or use case. Relays pass the attempt cap
   (`TestOutboxRelaysPassAttemptCap`).
4. **No calls to sibling contexts, ever.** Product classifications, allocated
   order lines and manifested work units are event-fed LOCAL COPIES (ADR
   0003) from `warehouse.product-master.events`,
   `warehouse.order-management.events` and `warehouse.fulfillment.events`.
   Each consumer group comes from its env var, never a literal
   (`TestKafkaConsumerGroupNeverHardcodedInline`).
5. **Disposition is immutable** (ADR 0002): outcome = f(grade, product class,
   policy version) and is recorded with the version used; an override is a NEW
   superseding Disposition with a mandatory reason. `RESTOCK` only for grade A
   or B; `ReturnLineStaged` is raised once per RESTOCK. `DispositionPolicy`
   versions are append-only. A receipt line references an RMA line or is
   explicitly `UNEXPECTED` (raises `UnexpectedReturnReceived`).
6. **The stow is not ours.** A RESTOCK is handed over as `ReturnLineStaged`;
   inventory-storage stages the stock and the stow stays an RF action there.
   Never model stock or bin moves here.
7. **Every POST/PUT is behind the `Idempotency-Key` middleware** (ADR 0009,
   `internal/adapters/inbound/http/idempotency.go`); a key reused with a
   different body is 409 `idempotency-key-reused`. A new write route goes in
   the same router group.
8. **No auth layer on REST, MCP or A2A** (fleet-wide revert 2026-09-11):
   never add bearer/JWT/API-key middleware (`TestNoAuthMiddlewareReintroduced`).
   MCP and A2A are read-only (ADR 0008, ADR 0010;
   `TestA2AAgentIsReadOnlyAndHasNoOutboundClient`).
9. **Real Postgres and real Kafka only.** No in-memory or log-only mode in a
   runtime binary (`TestRuntimeBinaryHasNoInMemoryAdapters`);
   `internal/adapters/outbound/memory` holds test doubles only. Integration
   tests boot their own containers via testcontainers.
10. **Analytics is a separate read side** (ADR 0007): own topic, own database
    (`ANALYTICS_DATABASE_URL`), `cmd/returns-projector` is its only writer,
    `cmd/returns-reports` only reads.
11. **An ADR comes first** for a new cross-context contract, a new consumed
    topic, a write surface for agents or any auth. ADRs live in `docs/adr/`.

## Commands

```bash
make check-fast    # fmt-check + vet + arch-test + tests of changed packages: run before saying "done"
make check         # fmt-check vet build lint test
make check-all     # check + coverage (90% gate on domain+application) + arch-test + bdd
make integration   # real Postgres/Kafka via testcontainers (needs Docker)
make mutation      # gremlins on each domain package (CI job mutation-fast, blocking)
make mutation-full # gremlins on internal/domain (CI job mutation, weekly)
make api-lint      # spectral on apis/openapi.yaml + apis/asyncapi.yaml
make vuln          # govulncheck
make guide-lint    # these guides: skills load, references resolve, context budget
make harness-test  # unit tests of the agent hooks
```

Run locally: `DATABASE_URL=... KAFKA_BROKERS=...
PRODUCT_CLASSIFICATION_CONSUMER_GROUP=... ORDER_LINES_CONSUMER_GROUP=...
MANIFEST_CONSUMER_GROUP=... go run ./cmd/api` (REST `:8080`; all five are
required and boot fails naming every missing one). `cmd/mcp` (`:8090`) and
`cmd/a2a` (`:8093`, also `A2A_AGENT_URL`) need `DATABASE_URL`;
`cmd/returns-projector` (`:8091`) and `cmd/returns-reports` (`:8092`) need
`ANALYTICS_DATABASE_URL`. Full table: `README.md`.

## Where things are written down (read before editing the matching area)

- `.claude/rules/domain-model.md`: ubiquitous language, aggregates, events,
  use cases. `.claude/rules/rest-api.md`: routes and conventions.
  `.claude/rules/integration-events.md`: published/consumed events, outbox,
  consumer rules.
- `.claude/rules/fleet/*.md`: fleet-wide rules copied from the harness
  template; never edit them here.
- `docs/adr/`: decisions (index: `docs/adr/index.md`). `apis/openapi.yaml`,
  `apis/asyncapi.yaml`: contracts. Operating docs: `docs/docs/operations/`
  (configuration, runbook, observability, troubleshooting, analytics),
  `docs/docs/development/testing.md`, `docs/docs/mcp/tools.md`. The REST
  pages under `docs/docs/api-reference/rest/` are generated (`docs-api-drift`
  CI job): never hand-edit them. `HARNESS.md`: what each sensor runs and why.
  `INCIDENTS.md`: the fleet incident log.
- REST guideline adoption: ADR 0009; the Spectral `rest-guideline` ruleset
  (`.spectral.yaml`) runs in the `api-lint` CI job.

harness-template: v4
(hooks, guide-lint).

<!-- harness:scoped-rules:start (generated by tools/migrate_v3.py in warehouse-harness-template; do not hand-edit) -->
## Scoped rules and harness

Claude Code loads each rule below automatically when you touch the matching paths. OpenCode and Codex do NOT: read the rule BEFORE editing matching files.

| When touching | Read |
|---|---|
| `internal/domain/**`, `internal/application/**`, `features/**` | `.claude/rules/domain-model.md` |
| `internal/adapters/**/kafka/**`, `internal/adapters/outbound/events/**`, `apis/asyncapi*` | `.claude/rules/integration-events.md` |
| `internal/adapters/inbound/http/**`, `apis/openapi*.yaml`, `apis/openapi/**` | `.claude/rules/rest-api.md` |

Hooks (`scripts/harness/hook.py`, wired for Claude Code, Codex and OpenCode) block pushes to develop/main, `--no-verify`, bare `rm -rf`, and edits to generated files, and feed gofmt/vet findings back after each edit. Before saying "done" run `make check-fast`; the full gate is `make check-all`. `HARNESS_OFF=1` disables the hooks when debugging the harness itself.
<!-- harness:scoped-rules:end -->
