# network_advice_rules.feature — the documentation-derived rules of the
# network return-advice seam (ADR 0012: the return-advices poller, and
# ADR 0013: retry the refusals the network releases). Every rule below is
# written in those ADRs and in docs/docs/operations/runbook.md; before this
# file none of them had a godog scenario. The network is the scripted
# gateway double (the real client, its derived idempotency key and its
# refusal-class table are the adapter's own spec-pinned tests).
Feature: Poll the network's return advices and report graded outcomes

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line

  # ADR 0012 Decision 2: the poller's first sight persists the advice,
  # keyed authorizationId, and re-delivery refreshes the network-owned
  # facts only — the outcome columns are ours and are never clobbered.
  Scenario: The first poll persists an advice keyed by its authorization id
    Given the network hands the poller an advice
      | field           | value                     |
      | authorizationId | net-auth-101              |
      | order           | net-ord-9001              |
      | group           | 42                        |
      | vendor          | vendor-42                 |
      | node            | node-7                    |
      | disposition     | NEAREST_RETURNS_CENTER    |
      | method          | POSTAL                    |
      | state           | AUTHORIZED                |
      | tracking        | TRK-8801                  |
      | readyAt         | 2026-10-21T08:40:00Z      |
      | lines           | SKU-NET-1:3:owner-a,SKU-NET-2:1: |
    When a poll tick runs
    Then the poll tick saw 1 advice and applied 1
    And the advice net-auth-101 is recorded with state AUTHORIZED, disposition NEAREST_RETURNS_CENTER and tracking TRK-8801
    And the advice net-auth-101 has 2 authorized lines
    And the advice net-auth-101 outcome state is none

  Scenario: A re-delivered advice refreshes the network's facts and never touches our outcome
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:2:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 2   | net-auth-101 |
    And line 1 is inspected with grade A and qty 2
    And the advice net-auth-101 outcome state is PENDING
    When the network hands the poller an advice
      | field           | value                     |
      | authorizationId | net-auth-101              |
      | order           | net-ord-9001              |
      | node            | node-7                    |
      | disposition     | KEEP                      |
      | method          | POSTAL                    |
      | state           | OUTCOME_RECORDED          |
      | tracking        | TRK-8801,TRK-8802         |
      | readyAt         | 2026-10-21T08:50:00Z      |
      | lines           | SKU-NET-1:2:owner-a       |
    And a poll tick runs
    Then the advice net-auth-101 is recorded with state OUTCOME_RECORDED, disposition KEEP and tracking TRK-8801,TRK-8802
    And the advice net-auth-101 outcome state is PENDING
    And one outcome is pending with lines
      | item      | owner   | restockable | unsellable |
      | SKU-NET-1 | owner-a | 2           | 0          |

  # poll_return_advices.go: the window starts at the watermark minus the
  # two-minute overlap; the zero time when the copy is empty.
  Scenario: Every poll looks back two minutes from the newest advice it holds
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:         |
    When a poll tick runs
    Then poll number 1 started from the beginning
    When a poll tick runs
    Then poll number 2 started from 2026-10-21T08:38:00Z

  # ADR 0012 Decision 5: the outcome is composed when every line of a
  # receipt opened against the advice is graded — RESTOCK units are
  # restockable, every other outcome unsellable, ownerRef from the advice's
  # matching line. Decision 4: the network disposition is an input; OUR
  # DispositionPolicy decides the quantities.
  Scenario: A receipt against an advice composes its outcome when its last line is graded
    Given the network hands the poller an advice
      | field           | value                     |
      | authorizationId | net-auth-101              |
      | order           | net-ord-9001              |
      | node            | node-7                    |
      | disposition     | NEAREST_RETURNS_CENTER    |
      | method          | POSTAL                    |
      | state           | AUTHORIZED                |
      | readyAt         | 2026-10-21T08:40:00Z      |
      | lines           | SKU-NET-1:3:owner-a,SKU-NET-2:1: |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 2   | net-auth-101 |
      | 2    | SKU-NET-2 | 1   | net-auth-101 |
    When line 1 is inspected with grade A and qty 2
    Then no outcome is pending
    And the advice net-auth-101 outcome state is none
    When line 2 is inspected with grade C and qty 1
    Then the advice net-auth-101 outcome state is PENDING
    And the one pending outcome is for authorization net-auth-101 at node node-7 received at 2026-10-21T09:00:00Z
    And one outcome is pending with lines
      | item      | owner   | restockable | unsellable |
      | SKU-NET-1 | owner-a | 2           | 0          |
      | SKU-NET-2 |         | 0           | 1          |

  # ADR 0012 Decision 5: "a sku the advice never authorized is submitted
  # anyway — goods on the dock are never turned away."
  Scenario: A sku the advice never authorized is submitted anyway
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
      | 2    | SKU-STRAY | 2   | net-auth-101 |
    When line 1 is inspected with grade A and qty 1
    And line 2 is inspected with grade B and qty 2
    Then one outcome is pending with lines
      | item      | owner   | restockable | unsellable |
      | SKU-NET-1 | owner-a | 1           | 0          |
      | SKU-STRAY |         | 0           | 2          |

  # ADR 0012's known gap, as a rule: the first completed receipt of an
  # advice wins, once; a later parcel is processed locally and reported
  # never.
  Scenario: The first completed receipt of an advice wins and a second parcel adds nothing
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:2:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade A and qty 1
    And time advances 1 hours
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    When line 1 is inspected with grade C and qty 1
    Then one outcome is pending with lines
      | item      | owner   | restockable | unsellable |
      | SKU-NET-1 | owner-a | 1           | 0          |
    And the one pending outcome is for authorization net-auth-101 at node node-7 received at 2026-10-21T09:00:00Z

  # ADR 0012 Decision 3: nothing in the receipt or inspection path changes
  # for non-network returns — a receipt of no advice composes no outcome.
  Scenario: A receipt of no advice composes no outcome
    Given order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 2 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 2   | rma-0001 |
    When line 1 is inspected with grade A and qty 2
    Then the disposition of line 1 is RESTOCK
    And no outcome is pending

  # ADR 0012 Decision 5: PENDING -> submit (202 + transaction id) ->
  # SUBMITTED -> settle SUCCEEDED (terminal) via the transaction reads.
  Scenario: An accepted outcome submits, then settles with the network's transaction verdict
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade A and qty 1
    And the network accepts the outcome of net-auth-101 with transaction tx-77
    When a drain tick runs
    Then the drain tick submitted 1, settled 0 and failed 0
    And the advice net-auth-101 outcome state is SUBMITTED
    And the one pending outcome carries transaction tx-77
    When the network transaction tx-77 is PROCESSING
    And a drain tick runs
    Then the advice net-auth-101 outcome state is SUBMITTED
    When the network transaction tx-77 is SUCCEEDED
    And a drain tick runs
    Then the drain tick submitted 0, settled 1 and failed 0
    And the advice net-auth-101 outcome state is SUCCEEDED
    And no outcome is pending

  Scenario: A failed transaction records the network's errors terminally
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade C and qty 1
    And the network accepts the outcome of net-auth-101 with transaction tx-88
    And the network transaction tx-88 is FAILED with errors ack-window-closed: the acknowledgement window closed
    When a drain tick runs
    And a drain tick runs
    Then the drain tick submitted 0, settled 0 and failed 1
    And the advice net-auth-101 outcome state is FAILED
    And the recorded refusal of net-auth-101 mentions ack-window-closed
    And no outcome is pending

  # ADR 0013 Decision 3: a released refusal keeps the outcome PENDING,
  # noted for the operator; the next tick submits the SAME body under the
  # same key, and an accepted submission clears the note.
  Scenario: A refusal the network releases keeps the outcome pending under the same body
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade A and qty 1
    And the network refuses the outcome of net-auth-101 with slug not-found as released
    And the network accepts the outcome of net-auth-101 with transaction tx-99
    When a drain tick runs
    Then the drain tick submitted 0, settled 0 and failed 0
    And the drain tick released net-auth-101 for not-found
    And the advice net-auth-101 outcome state is PENDING
    And the recorded refusal of net-auth-101 mentions not-found
    When a drain tick runs
    Then the drain tick submitted 1, settled 0 and failed 0
    And the advice net-auth-101 outcome state is SUBMITTED
    And the recorded refusal of net-auth-101 is empty
    And the network received 2 identical outcome bodies for net-auth-101

  # ADR 0013 Decision 5: the retry is bounded by time — 24 h from the
  # instant the outcome was composed; past it the released refusal is
  # recorded FAILED with its text.
  Scenario: A released refusal past the retry window is recorded failed
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade A and qty 1
    And the network refuses the outcome of net-auth-101 with slug vendor-suspended as released
    And the network refuses the outcome of net-auth-101 with slug vendor-suspended as released
    When a drain tick runs
    Then the advice net-auth-101 outcome state is PENDING
    Given the clock moves past the retry window
    When a drain tick runs
    Then the drain tick submitted 0, settled 0 and failed 1
    And the advice net-auth-101 outcome state is FAILED
    And the recorded refusal of net-auth-101 mentions vendor-suspended
    And no outcome is pending

  # ADR 0013's table: FINAL refusals (partial-acknowledgement,
  # ack-window-closed, the key problems) are stored and replayed by the
  # network — recording them FAILED is correct at once.
  Scenario: A final refusal is recorded failed at once
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade A and qty 1
    And the network refuses the outcome of net-auth-101 with slug partial-acknowledgement as final
    When a drain tick runs
    Then the drain tick submitted 0, settled 0 and failed 1
    And the advice net-auth-101 outcome state is FAILED
    And the recorded refusal of net-auth-101 mentions partial-acknowledgement
    And no outcome is pending

  # ADR 0013 Decision 2: unknown is terminal — a slug that is not in the
  # table never loops.
  Scenario: A refusal whose slug nobody knows is terminal
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade A and qty 1
    And the network refuses the outcome of net-auth-101 with slug solar-flare as final
    When a drain tick runs
    Then the drain tick submitted 0, settled 0 and failed 1
    And the advice net-auth-101 outcome state is FAILED
    And no outcome is pending

  # ADR 0013 Decision 2: a 4xx with no problem document is terminal.
  Scenario: A refusal with no problem document is terminal
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-101         |
      | order           | net-ord-9001         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-101 |
    And line 1 is inspected with grade A and qty 1
    And the network refuses the outcome of net-auth-101 without a problem document
    When a drain tick runs
    Then the drain tick submitted 0, settled 0 and failed 1
    And the advice net-auth-101 outcome state is FAILED
    And no outcome is pending

  # ADR 0012 Decision 6 and the drain's contract: one outcome's failure
  # never stops the drain — a dead network keeps its outcome PENDING while
  # every other outcome still steps forward.
  Scenario: A dead network keeps its outcome pending and never stops the drain
    Given the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-201         |
      | order           | net-ord-9002         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:40:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And the network hands the poller an advice
      | field           | value                |
      | authorizationId | net-auth-202         |
      | order           | net-ord-9003         |
      | node            | node-7               |
      | disposition     | KEEP                 |
      | method          | POSTAL               |
      | state           | AUTHORIZED           |
      | readyAt         | 2026-10-21T08:41:00Z |
      | lines           | SKU-NET-1:1:owner-a  |
    And a poll tick runs
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-201 |
    And line 1 is inspected with grade A and qty 1
    And a receipt is opened at site SITE1 with lines
      | line | sku       | qty | rma          |
      | 1    | SKU-NET-1 | 1   | net-auth-202 |
    And line 1 is inspected with grade A and qty 1
    And the network cannot reach the outcome of net-auth-201
    And the network accepts the outcome of net-auth-202 with transaction tx-31
    When a drain tick runs
    Then the drain tick failed with a transport error
    And the drain tick submitted 1, settled 0 and failed 0
    And the advice net-auth-201 outcome state is PENDING
    And the advice net-auth-202 outcome state is SUBMITTED
