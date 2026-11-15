# idempotency_rules.feature — wave 3: the Idempotency-Key behaviour of
# troubleshooting.md "Idempotency-Key behaviour worth knowing" and the REST
# problem table: the FIRST answer for a key is stored and replayed, 4xx
# problem documents included; replaying a stored success does not repeat the
# effect (no second receipt, no 409 line-already-inspected); a key belongs to
# one method+path+body; the key is 1-128 characters; every write needs one.
Feature: Idempotency-Key behaviour of the writes (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029

  Scenario: A refusal is stored too: its key replays it after the policy changed, a new key is judged afresh
    When a POST request is sent to "/returns" with key "r-1" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":6,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "return-not-authorizable"
    Given a policy version 2 is declared with window 30 days and cap 10 units per line
    When a POST request is sent to "/returns" with key "r-1" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":6,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response header "Idempotency-Replayed" is "true"
    And order ord-7 has 0 returns
    When a POST request is sent to "/returns" with key "r-2" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":6,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    And order ord-7 has 1 return

  Scenario: Replaying an inspection answers the stored 200 and changes nothing, a new key meets the 409
    Given a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":2}
    Then the response status is 200
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":2}
    Then the response status is 200
    And the response header "Idempotency-Replayed" is "true"
    And the response field "disposition.dispositionId" is disp-0001
    And the published receipt events are ReturnReceiptOpened,ReturnLineInspected
    And the published disposition events are DispositionDecided,ReturnLineStaged
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-2" and the JSON body {"grade":"A","quantity":2}
    Then the response status is 409
    And the response problem is "line-already-inspected"

  Scenario: Replaying a receipt opening returns the same receipt and opens no second one
    Given a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    When a POST request is sent to "/returns/rma-0001/receipts" with key "rec-1" and the JSON body {"siteId":"SITE1","lines":[{"lineNo":1,"sku":"SKU-1001","quantity":3,"kind":"MATCHED","rmaId":"rma-0001"}]}
    Then the response status is 201
    When a POST request is sent to "/returns/rma-0001/receipts" with key "rec-1" and the JSON body {"siteId":"SITE1","lines":[{"lineNo":1,"sku":"SKU-1001","quantity":3,"kind":"MATCHED","rmaId":"rma-0001"}]}
    Then the response status is 201
    And the response header "Idempotency-Replayed" is "true"
    And the response field "receiptId" is rcpt-0001
    And the published receipt events are ReturnReceiptOpened

  Scenario: A key is bound to its path: the same key and body on another version is a conflict
    When a PUT request is sent to "/disposition-policies/2" with key "pol-k" and the JSON body {"returnWindowDays":30,"maxUnitsPerLine":8,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}
    Then the response status is 201
    When a PUT request is sent to "/disposition-policies/3" with key "pol-k" and the JSON body {"returnWindowDays":30,"maxUnitsPerLine":8,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}
    Then the response status is 409
    And the response problem is "idempotency-key-reused"
    And the current policy is version 2 with window 30 days and cap 8 units per line

  Scenario: A key of 128 characters is accepted and one of 129 is refused as invalid
    When a POST request is sent to "/returns" with a key of 128 characters and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 201
    When a POST request is sent to "/returns" with a key of 129 characters and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 400
    And the response problem is "idempotency-key-invalid"
    And order ord-7 has 1 return

  Scenario Outline: Every write needs an Idempotency-Key, whatever the resource
    When a <method> request is sent to "<path>" with the JSON body {}
    Then the response status is 400
    And the response problem is "idempotency-key-required"

    Examples:
      | method | path                                  |
      | POST   | /returns/rma-0001/receipts            |
      | POST   | /receipts/rcpt-0001/lines/1/inspections |
      | POST   | /dispositions/disp-0001/overrides     |
      | PUT    | /disposition-policies/2               |
