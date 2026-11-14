# inspection_rules.feature — wave 3: InspectReturnLine (use-cases.md #6,
# business-context.md step 3, aggregate-design-canvas.md ReturnReceipt
# invariants 2-3, openapi POST .../inspections): the inspected quantity is
# [1, received], an inspection that is refused leaves the line open to a
# correct retry, the decision is made on the INSPECTED quantity, every
# RESTOCK line of a parcel is staged on its own, and the line's reason codes
# travel with the fact. The parcel: line 1 (rma-0001, SKU-1001, 3 units) and
# line 2 (UNEXPECTED, SKU-7777, 2 units).
Feature: Inspect a received line (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 3   | rma-0001 |
      | 2    | SKU-7777 | 2   |          |

  Scenario: A refused inspection leaves the line open, and the full received quantity is then accepted
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":4}
    Then the response status is 409
    And the response problem is "inspected-exceeds-received"
    And receipt line 1 is not inspected
    And no disposition events are published
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-2" and the JSON body {"grade":"A","quantity":3}
    Then the response status is 200
    And the response field "disposition.quantity" is 3
    And receipt line 1 carries grade A for 3 units
    And the published disposition events are DispositionDecided,ReturnLineStaged

  Scenario Outline: An inspected quantity below one or missing is refused and the line stays open
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-bad-<case>" and the JSON body <body>
    Then the response status is 400
    And the response problem is "invalid-quantity"
    And receipt line 1 is not inspected
    And no disposition events are published

    Examples:
      | case    | body                       |
      | zero    | {"grade":"A","quantity":0} |
      | omitted | {"grade":"A"}              |

  Scenario: A receipt line that does not exist is a typed 404 and nothing is recorded
    When a POST request is sent to "/receipts/rcpt-0001/lines/9/inspections" with key "insp-9" and the JSON body {"grade":"A","quantity":1}
    Then the response status is 404
    And the response problem is "receipt-not-found"
    And the response field "detail" is the receipt has no line 9
    And no disposition events are published

  Scenario: The decision and the staged stock use the inspected quantity, not the received one
    Given time advances 20 minutes
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"A","quantity":2}
    Then the response status is 200
    And the ReturnLineStaged event number 1 carries
      | field      | value                |
      | Quantity   | 2                    |
      | ReceivedAt | 2026-10-21T09:00:00Z |
    And the DispositionDecided event number 1 carries
      | field     | value                |
      | Quantity  | 2                    |
      | Outcome   | RESTOCK              |
      | DecidedAt | 2026-10-21T09:20:00Z |

  Scenario: ReturnLineInspected records the grade, the inspected quantity and the reason codes
    When a POST request is sent to "/receipts/rcpt-0001/lines/1/inspections" with key "insp-1" and the JSON body {"grade":"B","quantity":2,"reasonCodes":["OPENED_BOX","MISSING_PARTS"]}
    Then the response status is 200
    And the response field "reasonCodes.0" is OPENED_BOX
    And the response field "reasonCodes.1" is MISSING_PARTS
    And the response field "disposition.outcome" is REFURBISH
    And the ReturnLineInspected event number 1 carries
      | field       | value                      |
      | ReceiptID   | rcpt-0001                  |
      | LineNo      | 1                          |
      | SKU         | SKU-1001                   |
      | Quantity    | 2                          |
      | Grade       | B                          |
      | ReasonCodes | OPENED_BOX,MISSING_PARTS   |

  Scenario: Every RESTOCK line of a parcel is staged on its own, an unexpected line included
    When line 1 is inspected with grade A and qty 3
    And line 2 is inspected with grade A and qty 2
    Then the disposition of line 2 is RESTOCK
    And ReturnLineStaged is published 2 times
    And the ReturnLineStaged event number 2 carries
      | field         | value     |
      | DispositionID | disp-0002 |
      | LineNo        | 2         |
      | SKU           | SKU-7777  |
      | Quantity      | 2         |
