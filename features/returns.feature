# returns.feature — the authorization rule (ADR 0002 invariants 1 to 3):
# a return is authorized against a SHIPPED order line — allocated in the
# order-management copy and manifested in the fulfillment copy — under the
# current policy's window and cap, one open RMA per line. This is the
# contract the REST edge (a later step) puts in front of AuthorizeReturn.
Feature: Authorize a return against a shipped order line

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029

  Scenario: An allocated and manifested line authorizes one RMA
    When a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma status is AUTHORIZED
    And the rma sku is SKU-1001
    And the rma quantity is 3
    And the rma window ends at 2026-11-20T09:00:00Z
    And the one return of order ord-7 is rma-0001
    And the published rma events are ReturnAuthorized

  Scenario: A line that never shipped is refused
    Given order ord-9 line 1 is allocated sku SKU-2002
    When a return of 1 unit of order ord-9 line 1 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrLineNotManifested
    And order ord-9 has 0 returns
    And no rma events are published

  Scenario: A request over the policy cap is refused
    When a return of 6 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrQuantityExceedsPolicy
    And order ord-7 has 0 returns
    And no rma events are published

  Scenario: A line the allocations copy does not know fails closed
    When a return of 1 unit of order ord-404 line 1 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrOrderLineNotAllocated
    And order ord-404 has 0 returns
    And no rma events are published

  Scenario: A re-request for the same line is refused and changes nothing
    When a return of 2 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a return of 2 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrOpenRMAExists
    And the one return of order ord-7 is rma-0001
    And the rma status is AUTHORIZED
    And the published rma events are ReturnAuthorized
