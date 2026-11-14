# consumer_rules.feature — wave 3: the three copy-applying consumers
# (use-cases.md #1-#3, domain-events.md "Consumed", ADR 0003): last write wins
# on the allocation copy, a partial allocation refreshes only its own lines,
# the manifest's order_ref is reversed with the work-unit formula (split on
# the LAST "-line-"), a fact that can never apply is refused before any claim
# and applies nothing, a newer classification replaces an older one, and facts
# may arrive in any order — authorization fails CLOSED until both are there.
Feature: Consume the upstream facts (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line

  Scenario: A later allocation fact replaces the sku a line is authorized with
    Given an allocation fact evt-a1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    And an allocation fact evt-a2 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1002 |
    And order ord-7 line 2 shipped in package pkg-1029
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma sku is SKU-1002

  Scenario: A redelivered event id is ignored even when its payload differs
    Given an allocation fact evt-a1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    When an allocation fact evt-a1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-9999 |
    Then the consume outcome is duplicate
    Given order ord-7 line 2 shipped in package pkg-1029
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma sku is SKU-1001

  Scenario: A partial allocation refreshes only the lines it names
    Given an allocation fact evt-a1 for order ord-8 is consumed with lines
      | line | sku    |
      | 1    | SKU-A1 |
      | 2    | SKU-A2 |
    And an allocation fact evt-a2 for order ord-8 is consumed with lines
      | line | sku    |
      | 2    | SKU-B2 |
    And order ord-8 line 1 shipped in package pkg-81
    And order ord-8 line 2 shipped in package pkg-82
    When a return of 1 unit of order ord-8 line 1 for reason DAMAGED is requested at site SITE1
    And a return of 1 unit of order ord-8 line 2 for reason DAMAGED is requested at site SITE1
    And a GET request is sent to "/returns?orderRef=ord-8"
    Then the response list "items" has 2 items
    And the response field "items.0.sku" is SKU-A1
    And the response field "items.1.sku" is SKU-B2

  Scenario: The manifest's work unit is reversed on the last -line- so an order id may contain it
    Given an allocation fact evt-a1 for order ord-line-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    And a manifest fact evt-m1 for work unit ord-line-7-line-2 in package pkg-77 is consumed
    When a return of 1 unit of order ord-line-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma status is AUTHORIZED

  Scenario Outline: A manifest whose work unit is not the formula is refused and authorizes nothing
    Given an allocation fact evt-a1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    When a manifest fact evt-m1 for work unit <unit> in package pkg-1029 is consumed
    Then the command fails with ErrInvalidEvent
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrLineNotManifested

    Examples:
      | unit         |
      | ord-7        |
      | ord-7-line-0 |
      | -line-2      |

  Scenario: A manifest with no package id or no occurrence time is refused and authorizes nothing
    Given an allocation fact evt-a1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    When a manifest fact evt-m1 for work unit ord-7-line-2 without a package id is consumed
    Then the command fails with ErrInvalidEvent
    When a manifest fact evt-m2 for work unit ord-7-line-2 without an occurrence time is consumed
    Then the command fails with ErrInvalidEvent
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrLineNotManifested

  Scenario: An allocation fact with one bad line applies none of its lines
    When an allocation fact evt-a1 for order ord-7 is consumed with lines
      | line | sku      |
      | 1    | SKU-1001 |
      | 0    | SKU-1002 |
    Then the command fails with ErrInvalidEvent
    When a return of 1 unit of order ord-7 line 1 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrOrderLineNotAllocated

  Scenario: A classification fact without a version is refused before its id is claimed
    When a classification fact evt-c1 for sku SKU-1001 version 0 is consumed with tags class:Hazmat
    Then the command fails with ErrInvalidEvent
    When a classification fact evt-c1 for sku SKU-1001 version 1 is consumed with tags class:Hazmat
    Then the consume outcome is applied

  Scenario: A newer classification version replaces the older one the decision reads
    Given order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
    And a classification fact evt-c1 for sku SKU-1001 version 1 is consumed with tags hazmat,class:Hazmat
    And a classification fact evt-c2 for sku SKU-1001 version 2 is consumed with tags hazmat,class:Hazmat,dot:3
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is RESTOCK

  Scenario: Facts arriving out of order authorize nothing until both are there
    Given a manifest fact evt-m1 for work unit ord-7-line-2 in package pkg-1029 is consumed
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrOrderLineNotAllocated
    And order ord-7 has 0 returns
    When an allocation fact evt-a1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    And a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma status is AUTHORIZED
