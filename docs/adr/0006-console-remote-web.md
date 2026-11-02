# ADR 0006: the console remote (returns_mfe) — a read-and-command screen over the existing REST

## Status

Accepted (2026-10-21).

## Context

ADR 0001 scoped this context as a backend WMS tier with "no user-facing
screen of its own", and the repo's how-to-add-a-frontend-remote skill
accordingly said a `web/` directory would be drift until an ADR records why a
remote is needed and who consumes it. That decision is now taken: operators
walk the reverse flow (authorize → receive → inspect → disposition) through
the warehouse-console shell like every other context, and the shell mounts
per-context Module Federation remotes — returns-processing is the last
reverse-flow context without one. This ADR is that record.

## Decision

### The remote

`web/` is a Vite + React Module Federation remote, container `returns_mfe`,
exposing `./App` only (no props, relative routes), built on the fleet's
reference recipe (pick-face-replenishment) rather than re-derived:

- PINNED host/remote contract: federation container `returns_mfe`, gateway
  mount `/mfes/returns-processing/`, dev/preview port **4010** — deliberately
  off the console's 5181–5197 ladder, whose every slot an existing remote
  holds; 4010 collides with no remote and no service port.
- The API base is resolved at RUNTIME from the console's `/config.json`
  (`window.__WAREHOUSE_CONFIG__.apiOrigin` → `${apiOrigin}/api/returns-processing`);
  a production build throws when it is missing instead of falling back to a
  dev port. The remote calls ONLY its own context's API.
- Packaged as `nginxinc/nginx-unprivileged` (`web/Dockerfile`, `web/nginx.conf`),
  served under `/mfes/returns-processing/` by warehouse-infra's Nginx web
  gateway. Kong never serves these bytes; no ingress/httproute; the chart
  Service stays ClusterIP.
- `@warehouse/ui-kit` is a sibling checkout consumed as a named BuildKit
  context; the image never installs from a foreign `node_modules`.

### The screens — existing REST only, no new endpoints

Three surfaces, exactly what `apis/openapi.yaml` serves today:

1. **Returns** — `GET /returns?orderRef=` (the RMAs of one order; unknown
   order = empty list) plus `POST /returns` (authorize) as its write action.
2. **RMA detail** — `GET /returns/{rmaId}`; while AUTHORIZED, `POST
   /returns/{rmaId}/receipts` (open the receipt), then per received line
   `POST /receipts/{receiptId}/lines/{lineNo}/inspections` (grade; the policy
   disposition is decided in the same transaction) and, when the operator
   disagrees, `POST /dispositions/{dispositionId}/overrides`.
3. **Disposition policy** — `GET /disposition-policies/current` (read-only;
   versions are append-only and declared through the API, not the screen).

Every write POST mints a fresh `Idempotency-Key` (RFC 4122 v4 UUID) per
submission, as the API requires in every deployed mode. Where a screen wanted
data the API does not serve, the screen was cut, not the API: there is no
receipt or disposition GET, so the receipt lines and decisions shown are what
the write responses returned, held in the screen's state — a reload clears
that panel (the RMA card always re-reads). Declaring the next policy version
(`PUT /disposition-policies/{version}`) stays an API/MCP operation in v1.

### The deployment surface

The Helm chart grows a `frontend` component (`frontend-deployment.yaml`,
`frontend-service.yaml`, `_frontend.tpl`), default **off**; the Service
selects `component=frontend` so no Service ever selects two Deployments, and
the frontend Deployment renders NO env — nothing about the remote is
configured in the cluster. CI grows a `web` job (lint, typecheck, vitest,
production build, image build, nginx smoke check) on a plain `pull_request`
condition. Registering the remote in the console shell (its `vite.config.ts`
entry, route, nav) is the console repo's change, not this repo's.

## Consequences

- The how-to-add-a-frontend-remote skill's "not applicable" stance is
  superseded by this ADR: a `web/` directory and a frontend CI job are now in
  scope for this repo.
- Operators get the reverse flow in the console; the API surface is unchanged
  (openapi.yaml untouched — the docs-api-drift job stays green by
  construction).
- The receipt/disposition panel's no-reload limitation is accepted for v1;
  if operators need durable receipt reads, that is a NEW API decision (a
  `GET /receipts/{id}`) with its own ADR, not a screen change.
- The dev port 4010 must stay unique across the fleet's remotes; any
  collision is fixed HERE, in the remote's repo.
