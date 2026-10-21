# receipt.feature — the physical arrival (ADR 0002): one parcel, one site,
# every line matched to an RMA or explicitly UNEXPECTED — the presence of
# the rma id is the discriminator, so the refused third state cannot be
# expressed — and the inspection rule: a line is inspected exactly once,
# for at most the received quantity. The UnexpectedReturnReceived fact is
# the one exception-management opens a case on (a later consumer step).
Feature: Receive and inspect returned goods

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1

  Scenario: One parcel carries matched and unexpected lines
    When a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
      | 2    | SKU-7777 | 2   |          |
    Then the receipt has 2 lines
    And receipt line 1 is MATCHED
    And receipt line 2 is UNEXPECTED
    And the receipt rma is rma-0001
    And the published receipt events are ReturnReceiptOpened,UnexpectedReturnReceived

  Scenario: A parcel with no RMA at all opens on unexpected lines only
    When a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma |
      | 1    | SKU-7777 | 1   |     |
    Then the receipt has 1 line
    And receipt line 1 is UNEXPECTED
    And the receipt rma is none
    And the published receipt events are ReturnReceiptOpened,UnexpectedReturnReceived

  Scenario: A line is inspected exactly once
    When a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    And line 1 is inspected with grade A and qty 2
    Then receipt line 1 carries grade A for 2 units
    And the published receipt events are ReturnReceiptOpened,ReturnLineInspected
    When line 1 is inspected with grade B and qty 1
    Then the command fails with ErrLineAlreadyInspected
    And receipt line 1 carries grade A for 2 units
    And the published receipt events are ReturnReceiptOpened,ReturnLineInspected

  Scenario: An inspection may not exceed the received quantity
    When a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    And line 1 is inspected with grade A and qty 4
    Then the command fails with ErrInspectedExceedsReceived
    And receipt line 1 is not inspected
    And the published receipt events are ReturnReceiptOpened
