# authorization_rules.feature — wave 3: the authorization rules written in
# the context documentation (business-context.md "The flow" step 1,
# use-cases.md AuthorizeReturn, aggregate-design-canvas.md ReturnAuthorization
# invariants 1-3, openapi POST /returns): the cap is inclusive, the window is
# measured from the authorization instant under the CURRENT policy, the
# rejection code rides the 409 detail, a refusal leaves no trace, the one-open
# rule is per order LINE, and the RMA's sku is the allocation's, not the
# caller's.
Feature: Authorization rules of a return (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029

  Scenario: A quantity exactly at the policy cap is authorized
    When a return of 5 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma status is AUTHORIZED
    And the rma quantity is 5
    And the published rma events are ReturnAuthorized

  Scenario: One unit over the cap is refused with QUANTITY_EXCEEDS_POLICY and leaves no trace
    When a POST request is sent to "/returns" with key "cap-6" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":6,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "return-not-authorizable"
    And the response field "detail" starts with QUANTITY_EXCEEDS_POLICY
    And order ord-7 has 0 returns
    And no rma events are published

  Scenario: A line that never shipped is refused with LINE_NOT_MANIFESTED and leaves no trace
    Given order ord-9 line 1 is allocated sku SKU-2002
    When a POST request is sent to "/returns" with key "ship-9" and the JSON body {"orderId":"ord-9","lineNo":1,"sku":"SKU-2002","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "return-not-authorizable"
    And the response field "detail" starts with LINE_NOT_MANIFESTED
    And order ord-9 has 0 returns
    And no rma events are published

  Scenario: A zero-day window still authorizes and ends at the authorization instant
    Given a policy version 2 is declared with window 0 days and cap 5 units per line
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma status is AUTHORIZED
    And the rma window ends at 2026-10-21T09:00:00Z

  Scenario: The window is measured from the authorization instant, not from the shipment
    Given time advances 10 days
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma window ends at 2026-11-30T09:00:00Z

  Scenario: The cap and the window come from the policy version declared last
    Given a policy version 2 is declared with window 7 days and cap 2 units per line
    When a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrQuantityExceedsPolicy
    When a return of 2 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma status is AUTHORIZED
    And the rma window ends at 2026-10-28T09:00:00Z

  Scenario: The one-open-RMA rule is per order line, not per order and not per line number
    Given order ord-7 line 3 is allocated sku SKU-2002
    And order ord-7 line 3 shipped in package pkg-1030
    And order ord-8 line 2 is allocated sku SKU-1001
    And order ord-8 line 2 shipped in package pkg-1031
    When a return of 2 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a return of 1 unit of order ord-7 line 3 for reason WRONG_ITEM is requested at site SITE1
    And a return of 1 unit of order ord-8 line 2 for reason DAMAGED is requested at site SITE1
    Then order ord-7 has 2 returns
    And order ord-8 has 1 return
    And the published rma events are ReturnAuthorized,ReturnAuthorized,ReturnAuthorized

  Scenario: The RMA carries the allocation's sku and reads back with its documented fields
    When a POST request is sent to "/returns" with key "rma-ord-7-2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-WRONG","quantity":3,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    When a GET request is sent to "/returns/rma-0001"
    Then the response status is 200
    And the response field "sku" is SKU-1001
    And the response field "status" is AUTHORIZED
    And the response field "version" is 1
    And the response field "quantity" is 3
    And the response field "siteId" is SITE1
    And the response field "reasonCode" is DAMAGED
    And the response field "windowEndsAt" is 2026-11-20T09:00:00Z
    And the response has no field "rejectionCode"

  Scenario: ReturnAuthorized carries the line, the allocation's sku and the window
    When a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the ReturnAuthorized event number 1 carries
      | field      | value                |
      | RMAID      | rma-0001             |
      | OrderRef   | ord-7-line-2         |
      | SKU        | SKU-1001             |
      | Quantity   | 3                    |
      | Site       | SITE1                |
      | ReasonCode | DAMAGED              |
      | WindowEnds | 2026-11-20T09:00:00Z |
      | Authorized | 2026-10-21T09:00:00Z |
      | Version    | 1                    |

  Scenario Outline: A request outside the documented shape is refused and persists nothing
    When a POST request is sent to "/returns" with key "bad-<case>" and the JSON body <body>
    Then the response status is 400
    And the response problem is "<slug>"
    And order ord-7 has 0 returns
    And no rma events are published

    Examples:
      | case              | slug                   | body                                                                                                          |
      | negative-quantity | invalid-quantity       | {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":-1,"siteId":"SITE1","reasonCode":"DAMAGED"}        |
      | blank-reason      | malformed-request-body | {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":1,"siteId":"SITE1","reasonCode":""}                |
      | unknown-field     | malformed-request-body | {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED","x":1}   |
