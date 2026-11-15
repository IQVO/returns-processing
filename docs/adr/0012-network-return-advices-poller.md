# ADR 0012: The network return-advices poller (the first vendor-connectivity seam)

## Status

Accepted (2026-11-14).

## Context

The retail network grew a customer-returns context that decides, at
authorization, where a shopper's return goes. The network's vendor-connectivity
service exposes that decision to operators as a poll seam (the same seam the
purchase-order integration uses): `GET /vendor/return-advices` hands the
operator's nodes their `AUTHORIZED` return advices — the authorization's lines
and, as reverse labels are issued, their tracking numbers — and
`POST /vendor/return-outcomes` reports the graded result back. An advice leaves
the poll once the network records its outcome or the authorization is
cancelled.

This service already owns facility receipt, grading and per-line disposition
execution (`POST /returns`, receipts, inspections, `DispositionPolicy`), but it
has never talked to any sibling over HTTP: everything it knows about the world
arrives as warehouse-fleet events into local copies (ADR 0003). A network
return is a NEW kind of input: the network authorizes it (an operator
authorization would be a second, competing decision), and the physical goods
arrive at our dock against that authorization.

The network's own disposition vocabulary (`KEEP`, `NEAREST_RETURNS_CENTER`,
`ORIGIN_NODE`) answers WHERE goods go and WHO pays — decided at authorization,
before shipping. Our `DispositionPolicy` answers WHAT HAPPENS to each received
unit — decided at inspection, from grade and product class. They are different
decisions at different times; neither implies the other.

## Decision

1. **One outbound HTTP poller, behind a config flag.**
   `internal/adapters/outbound/retailnetwork` is the fleet context's FIRST
   vendor-connectivity (network Vendor API) client. It polls
   `GET /vendor/return-advices` on an interval and submits outcomes with
   `POST /vendor/return-outcomes`, exactly like the purchase-order integration
   pattern. The package is the only place that may name the network's
   vocabulary (authorization id, vendor id, node id, transaction id). The
   poller runs only when `NETWORK_RETURNS_POLLER_ENABLED` is set; enabled
   requires `NETWORK_RETURNS_BASE_URL` and `NETWORK_RETURNS_VENDOR_ID`, and a
   missing one is a boot error naming it. The flag defaults OFF in the chart
   and is ON in the study cluster (the deploy's value, not the code's).

2. **A `ReturnAdviceView` read model keyed `authorizationId`.** The poller's
   first sight of an advice persists it (idempotent upsert by
   `authorizationId`): lines, disposition, method, node, tracking numbers,
   `readyAt`/`expiresAt`. Re-delivery refreshes the network-owned facts only;
   the outcome columns are ours and are never clobbered by a poll. This is a
   local read model fed by a poll, not by events — the honest equivalent of
   ADR 0003's copies for a context whose provider offers no topic to us.

3. **The receipt flow keys `rmaId = authorizationId`.** A network advice needs
   no local `ReturnAuthorization`: the operator opens the existing receipt
   flow `POST /returns/{rmaId}/receipts` with the advice's `authorizationId`
   as the `rmaId`, and inspects lines exactly as today. Nothing in the receipt
   or inspection path changes for non-network returns.

4. **The network disposition is an INPUT, never re-decided.** The advice's
   disposition is recorded on the view as the network's fact. Our
   `DispositionPolicy` still decides each line's outcome from grade and
   product class (ADR 0002); it does not re-run, veto or confirm the
   network's authorization-level disposition. `POST /returns` stays exactly
   as it is for non-network flows; the poller never calls it.

5. **On completion, submit the outcome.** When every line of a receipt opened
   against an advice is inspected, the advice's outcome is composed from that
   receipt's disposition decisions — `RESTOCK` units are `qtyRestockable`,
   every other outcome is `qtyUnsellable`; `ownerRef` comes from the advice's
   matching line by sku, and a sku the advice never authorized is submitted
   anyway (the network records it as unexpected; goods on the dock are never
   turned away). The outcome is stored `PENDING` first, then the poller tick
   submits it (`202` + transaction id), then settles the transaction to
   `SUBMITTED` or `FAILED` with the network's refusal recorded. The
   idempotency key is derived from the `authorizationId`, so a retry after a
   crash can never double-report. Submission is asynchronous and
   at-least-once: no network call happens inside a unit of work.

6. **Degradation is logged, not fatal.** A poll failure is a warning and the
   next tick retries; a refused submission is recorded on the view for the
   operator. A network outage must not restart the pod (unlike a dead Kafka
   consumer, a delayed poll freezes nothing the REST surface serves).

**Known gap (recorded, not solved):** an advice whose goods arrive in more
than one parcel submits its outcome when the FIRST parcel's receipt completes;
the network records the outcome and drops the advice from the poll, so later
parcels are processed locally (restock staging still works) but are not
reported to the network. An outcome-amendment seam is the follow-up if
multi-parcel returns become real.

## Consequences

- The service makes its first outbound HTTP integration; the flag keeps every
  existing deployment byte-identical until the study cluster opts in.
- A new migration adds the `return_advice_view` table (a read model with
  submission state — not an aggregate; no domain events, no outbox rows, no
  new CloudEvents types, no REST route changes).
- The operators' console shows advices through the view (deployed with the
  poller); the REST and MCP surfaces of this service are unchanged.
- The deploy needs the poller's env vars (flag, base URL, vendor id, optional
  interval) on `cmd/api` only.
