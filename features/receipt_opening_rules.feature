# receipt_opening_rules.feature — wave 3: OpenReturnReceipt (use-cases.md #5,
# business-context.md step 2, aggregate-design-canvas.md ReturnReceipt
# invariant 1, openapi POST /returns/{rmaId}/receipts): every line is MATCHED
# or UNEXPECTED, each UNEXPECTED line raises its own event, several RMAs may
# ride one parcel, and v1 does NOT move the RMA on receipt.
Feature: Open a return receipt (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1

  Scenario: Each UNEXPECTED line of a parcel raises its own UnexpectedReturnReceived
    When a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
      | 2    | SKU-7777 | 2   |          |
      | 3    | SKU-8888 | 1   |          |
    Then the published receipt events are ReturnReceiptOpened,UnexpectedReturnReceived,UnexpectedReturnReceived
    And the UnexpectedReturnReceived event number 2 carries
      | field      | value                |
      | ReceiptID  | rcpt-0001            |
      | LineNo     | 3                    |
      | SKU        | SKU-8888             |
      | Quantity   | 1                    |
      | SiteID     | SITE1                |
      | Received   | 2026-10-21T09:00:00Z |

  Scenario: ReturnReceiptOpened lists every line with its rma, empty for an unexpected one
    When a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
      | 2    | SKU-7777 | 2   |          |
    Then the ReturnReceiptOpened event number 1 carries
      | field     | value                                |
      | ReceiptID | rcpt-0001                            |
      | RMAID     | rma-0001                             |
      | SiteID    | SITE1                                |
      | Opened    | 2026-10-21T09:00:00Z                 |
      | Lines     | 1:SKU-1001:3:rma-0001;2:SKU-7777:2:  |

  Scenario: Several RMAs may ship in one parcel; each line keeps its own rma
    Given order ord-7 line 3 is allocated sku SKU-2002
    And order ord-7 line 3 shipped in package pkg-1030
    And a return of 1 unit of order ord-7 line 3 for reason WRONG_ITEM is requested at site SITE1
    When a POST request is sent to "/returns/rma-0001/receipts" with key "rec-1" and the JSON body {"siteId":"SITE1","lines":[{"lineNo":1,"sku":"SKU-1001","quantity":3,"kind":"MATCHED","rmaId":"rma-0001"},{"lineNo":2,"sku":"SKU-2002","quantity":1,"kind":"MATCHED","rmaId":"rma-0002"}]}
    Then the response status is 201
    And the response field "rmaId" is rma-0001
    And the response field "lines.0.rmaId" is rma-0001
    And the response field "lines.1.rmaId" is rma-0002
    And the published receipt events are ReturnReceiptOpened

  Scenario Outline: A line that is neither MATCHED with an rma nor UNEXPECTED is refused and nothing opens
    When a POST request is sent to "/returns/rma-0001/receipts" with key "rec-bad-<case>" and the JSON body {"siteId":"SITE1","lines":[<lines>]}
    Then the response status is 400
    And the response problem is "invalid-line"
    And no receipt events are published

    Examples:
      | case                 | lines                                                                                                                                              |
      | matched-without-rma  | {"lineNo":1,"sku":"SKU-1001","quantity":3,"kind":"MATCHED"}                                                                                        |
      | unexpected-with-rma  | {"lineNo":1,"sku":"SKU-1001","quantity":3,"kind":"UNEXPECTED","rmaId":"rma-0001"}                                                                  |
      | duplicate-line-no    | {"lineNo":1,"sku":"SKU-1001","quantity":2,"kind":"MATCHED","rmaId":"rma-0001"},{"lineNo":1,"sku":"SKU-1001","quantity":1,"kind":"MATCHED","rmaId":"rma-0001"} |

  Scenario: Opening a receipt does not move the RMA, so the line stays closed to a second RMA
    When a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
    And a GET request is sent to "/returns/rma-0001"
    Then the response field "status" is AUTHORIZED
    And the response field "version" is 1
    When a POST request is sent to "/returns" with key "again-1" and the JSON body {"orderId":"ord-7","lineNo":2,"sku":"SKU-1001","quantity":1,"siteId":"SITE1","reasonCode":"DAMAGED"}
    Then the response status is 409
    And the response problem is "open-rma-exists"
    And order ord-7 has 1 return
