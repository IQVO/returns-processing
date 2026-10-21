---
name: how-to-add-a-frontend-remote
description: Not applicable to returns-processing — a backend WMS context with no micro-frontend remote. Use when asked to add a web UI to this repo, to learn why there is none, or when porting the fleet's web/ remote recipe from a repo that has one.
---

<!-- TEMPLATE NOTE (warehouse-harness-template v2): adapt every repo-specific example in this file (file paths, type names, field names) to THIS repo real code. Do not copy-paste verbatim. -->

# How to add a frontend remote (not applicable to this repo)

returns-processing is a backend WMS bounded context (ADR 0001): Kafka
consumers, a transactional outbox, a REST surface for the reverse flow,
and no user-facing screen of its own. It has NO `web/` directory, no
Module Federation remote, and no frontend CI job — v1 of this service is
not planned to grow one (operators read the reverse flow through the
warehouse-console shell backed by other contexts).

If a task genuinely requires a UI here, that is an architectural
decision first: write an ADR recording why a remote is needed and who
consumes it, then copy the WHOLE recipe from a fleet repo that has a
working remote (pick-face-replenishment is the reference: its
`how-to-add-a-frontend-remote` skill, its web CI job, and its Docker
packaging) rather than re-deriving the fleet's known traps:

- the Vite config must stay in OBJECT form (callback form breaks
  `mergeConfig` in `vitest.config.ts` and kills the whole test suite);
- a tiny, relative `dist/remoteEntry.js` after building with the
  `/mfes/<context>/` base is CORRECT, not a bug;
- `@warehouse/ui-kit` is a sibling checkout consumed as a named BuildKit
  context, and the Docker build must NOT install from the committed
  lockfile (native bindings);
- the remote needs a real `test` script, or added tests silently never
  run;
- wiring into the deploy is manual (the infra repo's services map and
  this repo's Helm chart `frontend.enabled` toggle).

Until such an ADR exists, a change that adds a `web/` directory or a
frontend build job to this repo should be flagged in review as
out-of-scope drift from ADR 0001.
