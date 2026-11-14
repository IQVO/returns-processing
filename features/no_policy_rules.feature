# no_policy_rules.feature — wave 3: the documented behaviour of a service on
# which no DispositionPolicy was ever declared (openapi POST /returns: "the
# same 409 answers a request made before any disposition policy is declared";
# POST .../inspections: a 404 receipt-not-found "when no disposition policy
# was ever declared"). Both fail CLOSED and persist nothing.
Feature: A service with no declared policy fails closed (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z

  Scenario: A return is refused as not authorizable before any policy is declared
    Given order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    When a POST request is sent to "/returns" with key "early-1" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "return-not-authorizable"
    And order ord-7 has 0 returns
    And no rma events are published

  Scenario: An inspection is refused before any policy is declared and the line stays open
    Given a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma |
      | 1    | SKU-7777 | 2   |     |
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "early-2" and the JSON body {"grade":"A","quantity":2}
    Then the response status is 404
    And the response problem is "receipt-not-found"
    And receipt line 1 is not inspected
    And the published receipt events are ReturnReceiptOpened,UnexpectedReturnReceived
    And no disposition events are published
