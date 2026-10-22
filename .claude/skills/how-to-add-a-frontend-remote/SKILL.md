---
name: how-to-add-a-frontend-remote
description: Use when asked to change the warehouse-console remote (web/, returns_mfe) — its pinned contract, screens, nginx packaging and CI — or to add another screen over the existing REST.
---

# How to work on the frontend remote (web/, `returns_mfe`)

returns-processing ships a Module Federation remote (`web/`, ADR 0006):
the warehouse-console shell lazy-loads `returns_mfe/App` and mounts it under
`/returns-processing/*`. Three surfaces over the EXISTING REST (no new
endpoints — if a screen wants data the API does not serve, cut the screen):

- returns list by order (`GET /returns?orderRef=`) + authorize (`POST /returns`);
- RMA detail (`GET /returns/{rmaId}`) with the physical flow: open the receipt
  (`POST /returns/{rmaId}/receipts`), inspect each line
  (`POST /receipts/{id}/lines/{n}/inspect`, the disposition is decided in the
  same transaction) and override a decision
  (`POST /dispositions/{id}/override`). There is NO receipt/disposition GET:
  the panel holds the write responses in screen state; a reload clears it;
- the current disposition policy (`GET /disposition-policies/current`),
  read-only.

Every write POST mints a fresh `Idempotency-Key` (UUID) per submission — the
API requires it in every deployed mode.

## The pinned host/remote contract (change both repos together or not at all)

- federation container `returns_mfe`, exposes `./App` only, relative routes;
- gateway mount `/mfes/returns-processing/` (Vite `base`, nginx path);
- dev/preview port **4010** (off the console's 5181-5197 ladder; every slot
  there is taken). A collision is fixed HERE, never in the port map silently.

## The fleet traps this repo already handles (do not reintroduce)

- the Vite config stays in OBJECT form — the callback form breaks
  `mergeConfig` in a non-standalone vitest config and kills test suites
  (vitest.config.ts here is standalone, keep it that way);
- `@warehouse/ui-kit` is a sibling checkout consumed as a named BuildKit
  context (`docker build --build-context uikit=../../warehouse-ui-kit`);
  the image never copies a foreign node_modules (native bindings);
- the API base is resolved at runtime from the console's /config.json
  (`window.__WAREHOUSE_CONFIG__.apiOrigin`), never baked at build time;
- the `web` CI job runs on a PLAIN pull_request condition — no paths filter,
  no hashFiles at job level;
- the chart's `frontend` component is default-off, ClusterIP-only, and
  renders NO env; the web gateway in warehouse-infra owns the path.
