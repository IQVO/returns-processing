---
id: index
title: Architecture decision records
sidebar_label: ADR index
slug: /
---

# Architecture decision records

The decisions of `returns-processing`, in the order they were taken. The
ADR bodies are immutable; a changed decision gets a new ADR that supersedes
the old one. The *Status* column is copied from each ADR's own `## Status`
section.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](/docs/adr/0001-returns-processing-bounded-context) | returns-processing as the owner of the reverse flow (RMA, return receipt, disposition) | Accepted (2026-10-20) |
| [0002](/docs/adr/0002-aggregates-and-policy) | the ReturnAuthorization, ReturnReceipt and Disposition aggregates, their invariants, and the DispositionPolicy as data | Accepted (2026-10-21) |
| [0003](/docs/adr/0003-local-copies-and-consumed-contracts) | event-fed local copies and the consumed contracts | Accepted (2026-10-21) |
| [0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) | CloudEvents envelope and type catalogue | Accepted (2026-10-21) |
| [0005](/docs/adr/0005-transactional-outbox-and-relay) | the transactional outbox and its relay — events leave through the outbox only | Accepted (2026-10-21) |
| [0006](/docs/adr/0006-console-remote-web) | the console remote (returns_mfe) — a read-and-command screen over the existing REST | Accepted (2026-10-21) |
