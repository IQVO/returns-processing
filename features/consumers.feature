# consumers.feature — the ADR 0003 local copies fed through the same consume
# use cases the inbound Kafka adapters call (wave 2): one claim per
# CloudEvents id (a replay is a duplicate that changes nothing), invalid
# facts refused before any claim, and the consumed facts visible to the REST
# journeys that depend on them (authorization, classification-driven
# disposition).
Feature: Consume the upstream facts into the local copies

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line

  Scenario: An allocation fact feeds the copy once; its replay is a duplicate
    When an allocation fact evt-alloc-1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    Then the consume outcome is applied
    When an allocation fact evt-alloc-1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    Then the consume outcome is duplicate

  Scenario: A manifest fact feeds the copy once; its replay is a duplicate
    When a manifest fact evt-man-1 for work unit ord-7-line-2 in package pkg-1029 is consumed
    Then the consume outcome is applied
    When a manifest fact evt-man-1 for work unit ord-7-line-2 in package pkg-1029 is consumed
    Then the consume outcome is duplicate

  Scenario: A classification fact feeds the copy once; its replay is a duplicate
    When a classification fact evt-cls-1 for sku SKU-1001 version 1 is consumed with tags class:Hazmat
    Then the consume outcome is applied
    When a classification fact evt-cls-1 for sku SKU-1001 version 1 is consumed with tags class:Hazmat
    Then the consume outcome is duplicate

  Scenario: An allocation fact with no lines is refused before any claim
    When an allocation fact evt-alloc-2 for order ord-7 is consumed with lines
      | line | sku |
    Then the command fails with ErrInvalidEvent
    When an allocation fact evt-alloc-2 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    Then the consume outcome is applied

  Scenario: Facts consumed upstream authorize a return over the API
    When an allocation fact evt-alloc-1 for order ord-7 is consumed with lines
      | line | sku      |
      | 2    | SKU-1001 |
    And a manifest fact evt-man-1 for work unit ord-7-line-2 in package pkg-1029 is consumed
    And a POST request is sent to "/returns" with key "rma-ord-7-2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":2,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    And the response field "rmaId" is rma-0001
    And the response field "status" is AUTHORIZED

  Scenario: A consumed classification drives the disposition of an inspected line
    Given order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 1 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
    When a classification fact evt-cls-1 for sku SKU-1001 version 1 is consumed with tags hazmat,class:Hazmat
    Then the consume outcome is applied
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":1}
    Then the response status is 200
    And the disposition of line 1 is DISPOSE
    And ReturnLineStaged is published 0 times

  Scenario: An older classification version changes nothing at the store
    Given a policy is declared with window 30 days, cap 5 units per line and rule grade A class Hazmat outcome RTV
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 1 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
    When a classification fact evt-cls-2 for sku SKU-1001 version 2 is consumed with tags class:Hazmat
    Then the consume outcome is applied
    When a classification fact evt-cls-1 for sku SKU-1001 version 1 is consumed with tags class:Ambient
    Then the consume outcome is applied
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is RTV

  Scenario: A same-version classification under another id does not overwrite either
    Given a policy is declared with window 30 days, cap 5 units per line and rule grade A class Hazmat outcome RTV
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 1 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
    When a classification fact evt-cls-1 for sku SKU-1001 version 1 is consumed with tags class:Hazmat
    Then the consume outcome is applied
    When a classification fact evt-cls-2 for sku SKU-1001 version 1 is consumed with tags class:Ambient
    Then the consume outcome is applied
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is RTV
