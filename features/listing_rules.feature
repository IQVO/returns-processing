# listing_rules.feature — wave 3: use-cases.md ListReturnsByOrder and openapi
# GET /returns: the RMAs of one order come "by line then id, one keyset page at
# a time", `[]` for an unknown order and never a 404. The RMAs are authorized
# in the order 3, 1, 2 on purpose: the page order is the contract's (line,
# id), not the insertion order, so the index assertions below are stable.
Feature: List the returns of an order (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 1 is allocated sku SKU-1001
    And order ord-7 line 1 shipped in package pkg-1
    And order ord-7 line 2 is allocated sku SKU-1002
    And order ord-7 line 2 shipped in package pkg-2
    And order ord-7 line 3 is allocated sku SKU-1003
    And order ord-7 line 3 shipped in package pkg-3
    And a return of 1 unit of order ord-7 line 3 for reason DAMAGED is requested at site SITE1
    And a return of 1 unit of order ord-7 line 1 for reason DAMAGED is requested at site SITE1
    And a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1

  Scenario: Pages walk the order by line number, then continue from the cursor
    When a GET request is sent to "/returns?orderRef=ord-7&limit=2"
    Then the response status is 200
    And the response list "items" has 2 items
    And the response field "items.0.lineNo" is 1
    And the response field "items.1.lineNo" is 2
    When the next page of "/returns?orderRef=ord-7&limit=2" is requested
    Then the response status is 200
    And the response list "items" has 1 items
    And the response field "items.0.lineNo" is 3
    And the response field "items.0.rmaId" is rma-0001
    And the response has no field "nextCursor"

  Scenario: A page that exactly fills the limit has no next cursor
    When a GET request is sent to "/returns?orderRef=ord-7&limit=3"
    Then the response status is 200
    And the response list "items" has 3 items
    And the response has no field "nextCursor"

  Scenario: An unknown order lists nothing instead of answering 404
    When a GET request is sent to "/returns?orderRef=ord-404"
    Then the response status is 200
    And the response list "items" has 0 items
    When a GET request is sent to "/returns"
    Then the response status is 400
    And the response problem is "malformed-request-body"
