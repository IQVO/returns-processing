# ADR 0008: A2A inbound adapter — a read-only Agent2Agent agent over the same read use cases

## Status

Accepted (2026-10-30). Additive: the REST API, the MCP server, the
integration topic `warehouse.returns-processing.events` and its payloads are
unchanged. No migration. Follows the fleet's A2A rollout (reference:
workforce-management ADR 0039 and wes-work-planning ADR 0046); builds on
[ADR 0001](0001-returns-processing-bounded-context.md) (no live REST/MCP/A2A
call between contexts) and [ADR 0005](0005-transactional-outbox-and-relay.md)
(events leave through the outbox only). This context has no dedicated MCP ADR:
its read-only MCP charter is `docs/docs/mcp/tools.md`.

## Context

The fleet is adopting the Agent2Agent (A2A) protocol so that a bounded context
can **delegate** a goal to another context's agent. This context already
exposes its read use cases over REST and a read-only MCP server (`cmd/mcp`:
`get_return`, `list_returns_by_order`, `get_disposition_policy`). Three
transports now carry three different things and must not overlap:

| Transport | Carries |
| --- | --- |
| Kafka + CloudEvents | **facts** between contexts (ADR 0004, ADR 0005) |
| MCP | an agent's **tools** |
| A2A | **delegation** of a goal to another context's agent; never data sync |

## Decision

Add `internal/adapters/inbound/a2a`, a third driving adapter over the **same**
read use cases (`GetReturn`, `ListReturnsByOrder`, `GetDispositionPolicy`) as
the REST and MCP adapters, built on the official `a2aproject/a2a-go/v2` SDK
(pinned v2.7.0) and served by a new `cmd/a2a` binary: JSON-RPC 2.0 over HTTP on
port **8093** (`A2A_ADDR`), the Agent Card at `/.well-known/agent-card.json`,
the endpoint mounted at both `/` and `/a2a`, `GET /healthz` for probes.
`A2A_AGENT_URL` (the absolute http(s) URL the card advertises) is mandatory;
the binary refuses to boot without it.

### READ-ONLY, because the MCP charter is read-only

An A2A skill may change state only if this context's MCP surface already offers
that write. It offers none (`TestToolSurface` fails the build on any write-verb
tool), so the agent is read-only: **delegation never widens what the context
exposes**. Authorizing or rejecting a return, opening a receipt, inspecting a
line, overriding a disposition and declaring a policy stay on REST. `cmd/a2a`
wires no write use case, no unit of work, no event publisher and no outbox, so
it can never insert an outbox row; it starts no relay, runs no local-copy
consumer and never dials Kafka. An allowlist fitness test
(`internal/architecture/a2a_fitness_test.go`) pins the use cases the adapter
and `cmd/a2a` may reference (the three reads), so a write added under any name
fails the build.

### Two curated skills, not one per endpoint

| Skill | Kind | Derived from | What it does |
| --- | --- | --- | --- |
| `return_status` | read | `get_return` + `list_returns_by_order` | Where does a return stand: by exactly one of `rmaId` (one RMA) or `orderId` (every RMA of the order). |
| `disposition_policy` | read | `get_disposition_policy` | The policy table: current when `version` is omitted, a declared version otherwise (the policy is append-only). |

The two return reads are folded into one goal-shaped skill because a delegating
agent asks one question ("where is this return?") and only the key differs.

### Message-only, answered in-band

Both skills are transactional request/response reads, so the agent is
message-only (no Task lifecycle). Domain rejections and malformed requests are
**answered** as RFC 7807-shaped `problem`s inside the message envelope, never
raised as JSON-RPC transport errors. The classification is the REST adapter's
for the errors a read can raise: `404` for a miss of the addressed resource
(`rma-not-found`, `policy-not-found`, same slugs and titles as REST), `400` for
request-shape problems (malformed id, unknown skill, unknown or conflicting
argument), `500` for anything unrecognised (logged, never leaked). REST's
`409` class belongs to writes and its `503` is the `/readyz` dependency check,
neither of which is a request outcome of a read.

### Strict input

The first message part carries `{"skill":"<id>","arguments":{...}}` as a Data
part (or a Text part holding the same JSON). Decoding rejects unknown fields
and trailing data, so a misspelled `versio` cannot silently fall back to the
current policy. (Go's `encoding/json` matches names case-insensitively, so only
genuinely unknown names are refused.) Ids follow the REST shape (1..64
characters, no white space, slash or NUL byte).

### Facts still travel on Kafka

A2A never becomes a data-sync channel, and this agent has no write that could
produce a fact. It also contains no outbound A2A client: this context keeps
event-fed local copies instead of calling siblings (ADR 0003).

### Unauthenticated, like REST and MCP

The card declares no security schemes: this surface is unauthenticated by the
same fleet-wide decision as REST and MCP (2026-09-11). Access control is the
in-cluster ClusterIP boundary; schemes are added to the card, not a redesign,
if that decision is ever reversed.

## Consequences

- A fourth deployable (`/app/a2a`) in the same image and an `a2a.*` chart block
  (`<release>-a2a`, default **off**), exposed through Kong at
  `/api/a2a/returns-processing` rewritten to `/`, so the card stays reachable
  under the same prefix. `A2A_AGENT_URL` defaults to the release's in-cluster
  Service DNS name and is overridable via `a2a.agentUrl`.
- Port 8093 is the fleet's A2A port (8080 api, 8090 mcp, 8091 projector admin,
  8092 reports).
- A future write skill needs its own decision: the MCP charter must offer the
  write first, it must run through the same use case and transactional outbox,
  and the allowlist fitness test must be widened deliberately.
