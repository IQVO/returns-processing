# disposition.feature — the decision (ADR 0002): the pure policy function
# decides per (grade x classification) under the current policy version —
# the class-specific rule beats the per-grade default, the guardrails fail
# closed (a hazmat SKU without a DOT rating never restocks), a RESTOCK
# stages the line exactly once and a replay does not re-raise, and an
# explained override supersedes the policy decision without mutating it.
Feature: Decide and override the disposition of an inspected line

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 4 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
      | 2    | SKU-1001 | 1   | rma-0001 |
      | 3    | SKU-1001 | 1   | rma-0001 |
      | 4    | SKU-1001 | 1   | rma-0001 |

  Scenario: The policy table decides per grade
    When line 1 is inspected with grade A and qty 1
    And line 2 is inspected with grade B and qty 1
    And line 3 is inspected with grade C and qty 1
    And line 4 is inspected with grade SCRAP and qty 1
    Then the disposition of line 1 is RESTOCK
    And the disposition of line 2 is REFURBISH
    And the disposition of line 3 is LIQUIDATE
    And the disposition of line 4 is DISPOSE
    And the published disposition events are DispositionDecided,ReturnLineStaged,DispositionDecided,DispositionDecided,DispositionDecided
    And ReturnLineStaged is published 1 time

  Scenario: A class-specific rule beats the per-grade default
    Given a policy is declared with window 30 days, cap 5 units per line and rule grade A class Hazmat outcome RTV
    And sku SKU-1001 is classified with tags class:Hazmat
    And the current policy is version 2 with window 30 days and cap 5 units per line
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is RTV
    And the disposition of line 1 keeps grade A under policy version 2
    And the published disposition events are DispositionDecided
    And ReturnLineStaged is published 0 times

  Scenario: A hazmat SKU without a DOT rating never restocks
    Given sku SKU-1001 is classified with tags hazmat,class:Hazmat
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is DISPOSE
    And the published disposition events are DispositionDecided
    And ReturnLineStaged is published 0 times

  Scenario: A hazmat SKU with a DOT rating restocks
    Given sku SKU-1001 is classified with tags hazmat,class:Hazmat,dot:3
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is RESTOCK
    And the published disposition events are DispositionDecided,ReturnLineStaged
    And ReturnLineStaged is published 1 time

  Scenario: A RESTOCK stages exactly once and a replay does not re-raise
    When line 1 is inspected with grade A and qty 1
    Then the published disposition events are DispositionDecided,ReturnLineStaged
    And ReturnLineStaged is published 1 time
    When line 1 is inspected with grade A and qty 1
    Then the command fails with ErrLineAlreadyInspected
    And the published disposition events are DispositionDecided,ReturnLineStaged
    And ReturnLineStaged is published 1 time

  Scenario: An override supersedes the policy decision with a reason
    Given time advances 10 minutes
    When line 1 is inspected with grade A and qty 1
    And time advances 5 minutes
    And the disposition of line 1 is overridden to RTV with reason vendor-recall
    Then the disposition of line 1 is RTV
    And the disposition of line 1 supersedes disp-0001 with reason vendor-recall
    And the disposition of line 1 keeps grade A under policy version 1
    And the disposition of line 1 was decided at 2026-10-21T09:15:00Z
    And the disposition disp-0001 is still RESTOCK
    And the published disposition events are DispositionDecided,ReturnLineStaged,DispositionDecided
    And ReturnLineStaged is published 1 time
