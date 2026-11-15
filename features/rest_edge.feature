# rest_edge.feature — the HTTP edge of the API (wave 2): every operation of
# apis/openapi.yaml through the real chi router, the RFC 7807 problem
# catalogue of apis/openapi.yaml's response examples, and the fleet
# Idempotency-Key middleware over the memory store. The journeys reuse the
# domain-worded seed steps; the assertions read the wire.
Feature: The REST edge of the returns API

  Background:
    Given the clock is 2026-10-21T09:00:00Z

  Scenario: A policy is declared and read back over the API
    When a PUT request is sent to "/disposition-policies/1" with key "pol-1" and the JSON body {"returnWindowDays":30,"maxUnitsPerLine":5,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}
    Then the response status is 201
    And the response field "version" is 1
    And the response field "returnWindowDays" is 30
    When a GET request is sent to "/disposition-policies/current"
    Then the response status is 200
    And the response field "version" is 1
    And the response field "maxUnitsPerLine" is 5
    And the response list "rules" has 4 items

  Scenario: The current policy of an undeclared service is a typed 404
    When a GET request is sent to "/disposition-policies/current"
    Then the response status is 404
    And the response problem is "policy-not-found"

  Scenario: An incomplete policy table is refused
    When a PUT request is sent to "/disposition-policies/1" with key "pol-1" and the JSON body {"returnWindowDays":30,"maxUnitsPerLine":5,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"}]}
    Then the response status is 400
    And the response problem is "invalid-policy"

  Scenario: A write without an Idempotency-Key is refused before any use case runs
    When a POST request is sent to "/returns" with the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 400
    And the response problem is "idempotency-key-required"

  Scenario: A return is authorized, read and listed over the API
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    When a POST request is sent to "/returns" with key "rma-ord-7-2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":3,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    And the response header "Location" is "/returns/rma-0001"
    And the response field "rmaId" is rma-0001
    And the response field "status" is AUTHORIZED
    And the response field "sku" is SKU-1001
    And the response field "quantity" is 3
    And the response field "windowEndsAt" is 2026-11-20T09:00:00Z
    When a GET request is sent to "/returns/rma-0001"
    Then the response status is 200
    And the response field "orderId" is ord-7
    And the response field "lineNo" is 2
    When a GET request is sent to "/returns?orderRef=ord-7"
    Then the response status is 200
    And the response list "items" has 1 items
    And the response field "items.0.rmaId" is rma-0001

  Scenario: An idempotent replay answers the recorded response and changes nothing
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    When a POST request is sent to "/returns" with key "rma-ord-7-2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":3,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    When a POST request is sent to "/returns" with key "rma-ord-7-2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":3,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    And the response header "Idempotency-Replayed" is "true"
    And the response field "rmaId" is rma-0001
    And order ord-7 has 1 returns

  Scenario: A reused key with a different body is a conflict
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    When a POST request is sent to "/returns" with key "shared-key" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":3,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    When a POST request is sent to "/returns" with key "shared-key" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":2,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "idempotency-key-reused"

  Scenario: A malformed body is refused
    Given a policy is declared with window 30 days and cap 5 units per line
    When a POST request is sent to "/returns" with key "bad-1" and the JSON body {"orderId":"ord-7","lineNo":2,
    Then the response status is 400
    And the response problem is "malformed-request-body"

  Scenario: A zero quantity is refused
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    When a POST request is sent to "/returns" with key "bad-2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":0,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 400
    And the response problem is "invalid-quantity"

  Scenario: A line that never shipped is a typed conflict
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-9 line 1 is allocated sku SKU-2002
    When a POST request is sent to "/returns" with key "rma-ord-9-1" and the JSON body {"orderId":"ord-9","lineNo":1,"sku":"SKU-2002","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "return-not-authorizable"
    And order ord-9 has 0 returns

  Scenario: A second open RMA for the same line is a typed conflict
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    When a POST request is sent to "/returns" with key "k1" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":2,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    When a POST request is sent to "/returns" with key "k2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":2,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "open-rma-exists"
    And order ord-7 has 1 returns

  Scenario: An unknown RMA is a typed 404
    When a GET request is sent to "/returns/rma-404"
    Then the response status is 404
    And the response problem is "rma-not-found"

  Scenario: List paging parameters are validated
    When a GET request is sent to "/returns?orderRef=ord-7&limit=0"
    Then the response status is 400
    And the response problem is "invalid-limit"
    When a GET request is sent to "/returns?orderRef=ord-7&cursor=not-a-cursor"
    Then the response status is 400
    And the response problem is "invalid-cursor"

  Scenario: A receipt opens over the API against the path RMA
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    When a POST request is sent to "/returns/rma-0001/receipts" with key "rec-1" and the JSON body {"siteId":"SITE1","lines":[{"lineNo":1,"sku":"SKU-1001","quantity":3,"kind":"MATCHED","rmaId":"rma-0001"}]}
    Then the response status is 201
    And the response field "receiptId" is rcpt-0001
    And the response field "rmaId" is rma-0001
    And the response list "lines" has 1 items
    And the response field "lines.0.kind" is MATCHED

  Scenario: A receipt is accepted without an RMA lookup, as the spec documents
    Given a policy is declared with window 30 days and cap 5 units per line
    When a POST request is sent to "/returns/rma-404/receipts" with key "rec-1" and the JSON body {"siteId":"SITE1","lines":[{"lineNo":1,"sku":"SKU-1001","quantity":1,"kind":"MATCHED","rmaId":"rma-404"}]}
    Then the response status is 201
    And the response field "rmaId" is rma-404

  Scenario: A receipt with no lines is refused
    Given a policy is declared with window 30 days and cap 5 units per line
    When a POST request is sent to "/returns/rma-0001/receipts" with key "rec-1" and the JSON body {"siteId":"SITE1","lines":[]}
    Then the response status is 400
    And the response problem is "invalid-line"

  Scenario: An inspection over the API decides the disposition in the same answer
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":2,"reasonCodes":["DAMAGED"]}
    Then the response status is 200
    And the published receipt events are ReturnReceiptOpened,ReturnLineInspected

  Scenario: An unknown grade is refused
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"Z","quantity":1}
    Then the response status is 400
    And the response problem is "invalid-grade"

  Scenario: An inspection over the received quantity is a typed conflict
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":4}
    Then the response status is 409
    And the response problem is "inspected-exceeds-received"

  Scenario: A second inspection of the same line is a typed conflict
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    And line 1 is inspected with grade A and qty 1
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-2" and the JSON body {"grade":"B","quantity":1}
    Then the response status is 409
    And the response problem is "line-already-inspected"

  Scenario: An inspection of an unknown receipt is a typed 404
    Given a policy is declared with window 30 days and cap 5 units per line
    When a POST request is sent to "/receipts/rec-404/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":1}
    Then the response status is 404
    And the response problem is "receipt-not-found"

  Scenario: An override supersedes the decision over the API
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 1 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
    And line 1 is inspected with grade A and qty 1
    When a POST request is sent to "/dispositions/disp-0001/overrides" with key "ovr-1" and the JSON body {"outcome":"RTV","reason":"vendor-recall"}
    Then the response status is 200
    And the response field "outcome" is RTV
    And the disposition of line 1 is RTV

  Scenario: An override must say why
    Given a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 1 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
    And line 1 is inspected with grade A and qty 1
    When a POST request is sent to "/dispositions/disp-0001/overrides" with key "ovr-1" and the JSON body {"outcome":"RTV","reason":" "}
    Then the response status is 400
    And the response problem is "override-reason-required"

  Scenario: An override of an unknown disposition is a typed 404
    Given a policy is declared with window 30 days and cap 5 units per line
    When a POST request is sent to "/dispositions/disp-404/overrides" with key "ovr-1" and the JSON body {"outcome":"RTV","reason":"vendor-recall"}
    Then the response status is 404
    And the response problem is "disposition-not-found"
