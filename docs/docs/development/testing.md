---
id: testing
title: Testing
sidebar_label: Testing
sidebar_position: 1
---

# Testing

The gates, in the order you normally run them. `lefthook` runs `make check` on commit and on push,
so most of this is enforced before a branch can leave the machine.

## Unit and fast gates

```bash
make check        # fmt-check + vet + build + golangci-lint + go test ./... -race
make check-fast   # fmt-check + vet + arch-test only (the tight loop while coding)
make check-all    # check + coverage + arch-test + bdd — run before pushing
```

## Coverage

`make coverage` gates at **90 %** over `./internal/domain/...` and `./internal/application/...`
and writes the number into the CI step summary. The domain packages are additionally held by
mutation testing: `make mutation` runs gremlins on the fast blocking subset
(`returnauthorization`, `returnreceipt`, `disposition`, `shared` — every domain package, each
mutated on its own, thresholds in `.gremlins.yaml`); `make mutation-full` is the exhaustive
scheduled run over all of `internal/domain`.

## Integration (testcontainers)

```bash
make integration   # go test -tags=integration ./... -race -count=1
```

Kafka and Postgres integration tests use **testcontainers only** — they boot their own containers
(`TestPostgresIntegrationTestsUseTestcontainers` enforces it), so no `DATABASE_URL`, no docker
compose and no skip-on-missing-env. Deliberately not part of `check` / `check-all`; CI runs it as
its own job.

## Acceptance (BDD) and architecture fitness

- `make bdd` — godog/Gherkin acceptance over `features/`: `returns.feature` (the authorization
  rule: allocated + manifested, window, cap, one open RMA per line), `receipt.feature` (matched /
  unexpected lines, inspect once) and `disposition.feature` (the Decide table, guardrails,
  overrides, staging once).
- `make arch-test` — the hexagonal fitness suite in `internal/architecture`: the domain imports
  nothing internal but the domain, no shipped binary imports the in-memory outbound adapters
  (`TestRuntimeBinaryHasNoInMemoryAdapters`), the outbox relay attempt cap is wired in the
  composition root, the Kafka writer is acks-all on the right topics, no Go files at the repo
  root, and `TestEventCatalogueMatchesContract` pins the CloudEvents catalogue
  ([ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue)) against
  `apis/asyncapi.yaml`.

The MCP surface is read-only by construction: `cmd/mcp` wires no write use case, no outbox and no
relay, and its three tools (`get_return`, `list_returns_by_order`, `get_disposition_policy`) are
pinned by tests ([MCP tools](/docs/mcp/tools)).

## Contracts and supply chain

```bash
make api-lint   # spectral on apis/openapi.yaml + apis/asyncapi.yaml (fail-severity=warn)
make vuln       # govulncheck — run after any go get / go mod tidy
```

## The docs site

```bash
cd docs
npm ci
npm run gen-api-docs   # regenerate docs/docs/api-reference/rest from apis/openapi.yaml
npm run build          # what the Docs workflow runs
```

The `docs-api-drift` CI job regenerates the REST reference on every PR and fails if it differs
from what is committed — never hand-edit the generated pages.
