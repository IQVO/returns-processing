---
id: ubiquitous-language
title: Ubiquitous language
sidebar_label: Ubiquitous language
sidebar_position: 9
---

# Ubiquitous language

The terms of the context and the code identifier that implements each one
(`.claude/rules/domain-model.md` is the rule; this page adds the tables and topics).

| Term | Meaning | Code |
| --- | --- | --- |
| ReturnAuthorization (RMA) | Permission to send one shipped order line back; carries the authorized quantity, the reason and the policy window; one open RMA per order line | `returnauthorization.RMA`, table `return_authorizations` |
| Order ref / line | The (order, line) a return is authorized against; the shipped fact is the work unit `{order_id}-line-{line_no}` | `shared.OrderLineRef`, `OrderLineRef.WorkUnit()` |
| Requested, Authorized, Rejected, Received, Closed | The RMA states; `REJECTED` and `CLOSED` are terminal | `returnauthorization.Status` |
| Rejection code | Why an RMA was refused: `LINE_NOT_MANIFESTED`, `RETURN_WINDOW_EXPIRED`, `OPEN_RMA_EXISTS`, `QUANTITY_EXCEEDS_POLICY` | `returnauthorization.RejectionCode` |
| Return window | `authorized_at + policy.return_window_days`; the goods must arrive inside it | `RMA.WindowEndsAt` |
| ReturnReceipt | The physical arrival of returned goods, against an RMA or with unexpected lines only | `returnreceipt.Receipt`, tables `return_receipts`, `return_receipt_lines` |
| Return line, MATCHED / UNEXPECTED | Every received line is MATCHED (carries its `rmaId`) or explicitly UNEXPECTED — never a silent third state | `returnreceipt.LineKind` |
| Grade | The inspection outcome of a line: `A`, `B`, `C`, `SCRAP`, with reason codes; recorded exactly once, quantity never above the received quantity | `returnreceipt.Grade` |
| Reason codes | Why a grade (e.g. `CUSTOMER_DAMAGE`, `OPENED_BOX`, `MISSING_PARTS`) | `ReturnLineInspected.reason_codes` |
| Disposition | The immutable decision for one inspected line; an override is a NEW disposition with `supersedes` and a mandatory reason | `disposition.Disposition`, table `dispositions` |
| Outcome | `RESTOCK`, `REFURBISH`, `LIQUIDATE`, `DISPOSE`, `RTV`; only RESTOCK touches a sibling | `disposition.Outcome` |
| DispositionPolicy | The versioned operator table: grade × product class → outcome, return window days, max units per line, veto outcome; append-only | `disposition.Policy`, table `disposition_policies` |
| Veto outcome | What a guardrail demotes a forbidden RESTOCK to (default `DISPOSE`) | `Policy.VetoOutcome` |
| Product class `*` | The per-grade default rule when no exact (grade, class) entry matches | `Policy.Rule` |
| Guardrails | Hard-coded safety, not operator tuning: RESTOCK only grade A/B, never a DISPOSE-classified SKU, never a hazmat SKU without a DOT rating | `disposition.Decide` |
| Staged line | A RESTOCK handed to inventory-storage (`ReturnLineStaged`); the stow stays an RF action there | `disposition.Staging`, inventory-storage's staged-receipt use case |
| Unexpected return | A received line with no RMA; opens a problem-solve case downstream | `UnexpectedReturnReceived`, exception-management |
| Local copy | An event-fed table this context reads instead of calling a sibling | `product_classes`, `allocated_lines`, `manifested_units` |
| Claim | The `(consumer, CloudEvents id)` row that makes a consumer effect idempotent, written in the same transaction as the effect | `processed_events`, `ports.ProcessedEvents` |
| Outbox | The transactional table of already-encoded CloudEvents waiting for the relay | `outbox`, `internal/application/outbox` |
| Idempotency key | The caller's `Idempotency-Key`; the first answer is stored and replayed | `idempotency_keys`, `inboundhttp.Idempotency` |
| Dead letter | A message moved to a `.dlq` topic: inbound poison rows, or outbox rows the encoder refuses | `inboundkafka.DLQ`, `warehouse.returns-processing.events.dlq` |
| Version | Optimistic-concurrency counter on every aggregate write (409 `concurrent-modification` on a lost update) | `RMA.Version`, `Disposition.Version` |
