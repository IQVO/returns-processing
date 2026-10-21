---
id: tools
title: MCP tools
sidebar_label: MCP tools
sidebar_position: 1
---

# MCP tools

`cmd/mcp` serves a **read-only** Model Context Protocol surface over the same read use cases the
REST adapter uses. It is a second, independent deployable (Helm chart component `mcp`, **disabled
by default**), so an agent host can answer questions about returns without touching the write path.

| | |
| --- | --- |
| Transport | Streamable HTTP at `MCP_ADDR` (default `:8090`), path `/mcp`; no stdio, no SSE |
| Health | `GET /healthz` open (JSON status) |
| Auth | none — fleet decision 2026-09-11; access control is the in-cluster ClusterIP boundary |
| Writes | impossible: no write use case is wired, so this binary can never insert into the outbox; it also never starts the relay, never runs consumers, never dials Kafka |
| Runtime | real Postgres only (`DATABASE_URL` required; no in-memory mode) |
| Env | `DATABASE_URL`, `MIGRATIONS_DATABASE_URL` (direct DSN for boot migrations; default `DATABASE_URL`), `MCP_ADDR` (`:8090`), `LOG_LEVEL` |

## Tools

Every tool is annotated read-only, idempotent and closed-world. `TestToolSurface`
(`internal/adapters/inbound/mcp/governance_test.go`) fails the build on any write-verb tool name,
and the exact registry is pinned by a golden file
(`internal/adapters/inbound/mcp/testdata/tool_registry.golden.json`).

### `get_return`

Read one return authorization (RMA) by id: the original order id and line number it was authorized
against, the work unit (`{order_id}-line-{line_no}`) the goods shipped in, the sku, quantity and
site, the reason code, the status (`REQUESTED`, `AUTHORIZED`, `REJECTED`, `RECEIVED`, `CLOSED`), the
rejection code on a refused return, the instant the return window ends, and the aggregate version.

- Input: `rma_id` (string, 1–64 chars, no white space, slash or NUL)
- Errors: `invalid-rma-id` (malformed), `rma-not-found` (unknown id)

### `list_returns_by_order`

List every RMA of one original order, ordered by line number then id. An order with no returns —
or an unknown order — answers an empty list, never an error. Each item has the same shape as
`get_return`.

- Input: `order_id` (string, 1–64 chars)
- Errors: `invalid-order-id`

### `get_disposition_policy`

Read the disposition policy table: the version, the return window in days, the max units per
return line, the veto outcome a guardrail demotes to (default `DISPOSE`), the declared instant and
the rule table mapping each inspection grade (A, B, C, SCRAP) and product class to an outcome
(RESTOCK, REFURBISH, LIQUIDATE, DISPOSE, RTV); `"*"` is a rule's default product class.

- Input: `version` (integer, **optional**): omitted means the CURRENT version; a positive number
  reads that declared one (the policy is append-only: a change is a new version, never a rewrite)
- Errors: `policy-not-found` (no such version, or no policy declared yet)

A handler error becomes an `isError` tool result carrying the error text — never a transport
failure.

## Example call

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "list_returns_by_order",
    "arguments": {"order_id": "ord-7c9e"}
  }
}
```

```json
{
  "items": [
    {
      "rma_id": "rma-4d5e6f7a-8b9c-4d0e-a1f2-3b4c5d6e7f80",
      "order_id": "ord-7c9e",
      "line_no": 1,
      "sku": "SKU-1",
      "quantity": 2,
      "site_id": "WH1",
      "reason_code": "DAMAGED",
      "status": "AUTHORIZED",
      "window_ends_at": "2026-11-20T09:30:00Z",
      "version": 1
    }
  ]
}
```

## Who consumes this

`warehouse-ops-agent` gets its read-only returns tools over this server (planned, later wave — not
built yet). Writes (authorize, receive, inspect, override, declare policy) stay on REST and have no
MCP path by design.

Source: `cmd/mcp/main.go`, `cmd/mcp/router.go`, `internal/adapters/inbound/mcp/{server,tools}.go`.
