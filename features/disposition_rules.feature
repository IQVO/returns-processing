# disposition_rules.feature — wave 3: the Decide function, its guardrails and
# the override (aggregate-design-canvas.md "Disposition", ADR 0002 invariants
# 1-4, troubleshooting.md "Decisions look wrong", use-cases.md #6/#7). The
# standard table is A restocks, B refurbishes, C liquidates, SCRAP disposes
# (veto DISPOSE). One RMA of 4 units arrives as 4 one-unit lines, all SKU-1001.
Feature: Decision guardrails and overrides (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 4 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 1   | rma-0001 |
      | 2    | SKU-1001 | 1   | rma-0001 |
      | 3    | SKU-1001 | 1   | rma-0001 |
      | 4    | SKU-1001 | 1   | rma-0001 |

  Scenario: A class rule written for grade A leaves grade B of the same class on its default
    Given a policy is declared with window 30 days, cap 5 units per line and rule grade A class Hazmat outcome RTV
    And sku SKU-1001 is classified with tags class:Hazmat
    When line 1 is inspected with grade B and qty 1
    Then the disposition of line 1 is REFURBISH
    And the disposition of line 1 keeps grade B under policy version 2

  Scenario: A SKU of another class falls back to the per-grade default
    Given a policy is declared with window 30 days, cap 5 units per line and rule grade A class Hazmat outcome RTV
    And sku SKU-1001 is classified with tags class:Standard
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is RESTOCK
    And ReturnLineStaged is published 1 time

  Scenario Outline: The DOT hazard class decides whether a hazmat SKU may restock; 0 means no rating
    Given sku SKU-1001 is classified with tags <tags>
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is <outcome>
    And ReturnLineStaged is published <staged> times

    Examples:
      | tags         | outcome | staged |
      | hazmat,dot:0 | DISPOSE | 0      |
      | hazmat,dot:1 | RESTOCK | 1      |
      | dot:0        | RESTOCK | 1      |

  Scenario: A SKU the product master forces to DISPOSE never restocks
    Given sku SKU-1001 is classified with tags force:DISPOSE
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is DISPOSE
    And the published disposition events are DispositionDecided
    And ReturnLineStaged is published 0 times

  Scenario: The grade ceiling for RESTOCK is B, even when the operator's table says RESTOCK for every grade
    Given a PUT request is sent to "/disposition-policies/2" with key "pol-2" and the JSON body {"returnWindowDays":30,"maxUnitsPerLine":5,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"RESTOCK"},{"grade":"C","productClass":"*","outcome":"RESTOCK"},{"grade":"SCRAP","productClass":"*","outcome":"RESTOCK"}]}
    And the response status is 201
    When line 1 is inspected with grade A and qty 1
    And line 2 is inspected with grade B and qty 1
    And line 3 is inspected with grade C and qty 1
    And line 4 is inspected with grade SCRAP and qty 1
    Then the disposition of line 1 is RESTOCK
    And the disposition of line 2 is RESTOCK
    And the disposition of line 3 is DISPOSE
    And the disposition of line 4 is DISPOSE
    And ReturnLineStaged is published 2 times

  Scenario Outline: A demoted RESTOCK falls back to the veto outcome the policy names
    Given a PUT request is sent to "/disposition-policies/2" with key "pol-2" and the JSON body {"returnWindowDays":30,"maxUnitsPerLine":5,"vetoOutcome":"<veto>","rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"RESTOCK"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}
    And the response status is 201
    When line 3 is inspected with grade C and qty 1
    Then the disposition of line 3 is <veto>
    And ReturnLineStaged is published 0 times

    Examples:
      | veto      |
      | RTV       |
      | LIQUIDATE |

  Scenario: The policy current at inspection decides, not the one current when the parcel arrived
    Given a policy version 2 is declared where grade A maps to REFURBISH by default
    When line 1 is inspected with grade A and qty 1
    Then the disposition of line 1 is REFURBISH
    And the disposition of line 1 keeps grade A under policy version 2
    And ReturnLineStaged is published 0 times

  Scenario: A decision keeps the version it used, and so does the override of it
    When line 1 is inspected with grade A and qty 1
    And a policy version 2 is declared where grade B maps to LIQUIDATE by default
    And line 2 is inspected with grade B and qty 1
    And the disposition of line 1 is overridden to RTV with reason vendor-recall
    Then the disposition of line 2 is LIQUIDATE
    And the disposition of line 2 keeps grade B under policy version 2
    And the disposition of line 1 keeps grade A under policy version 1

  Scenario: ReturnLineStaged carries the receipt's arrival time and the inspected line
    Given time advances 20 minutes
    When line 1 is inspected with grade A and qty 1
    Then the ReturnLineStaged event number 1 carries
      | field         | value                |
      | DispositionID | disp-0001            |
      | ReceiptID     | rcpt-0001            |
      | LineNo        | 1                    |
      | SKU           | SKU-1001             |
      | Quantity      | 1                    |
      | Condition     | Good                 |
      | ReceivedAt    | 2026-10-21T09:00:00Z |
      | RMAID         | rma-0001             |
      | SiteID        | SITE1                |

  Scenario: A RESTOCK override stages the line under its own disposition id
    When line 2 is inspected with grade B and qty 1
    And the disposition of line 2 is overridden to RESTOCK with reason inspector re-graded the unit as new
    Then the disposition of line 2 is RESTOCK
    And the disposition disp-0001 is still REFURBISH
    And ReturnLineStaged is published 1 time
    And the ReturnLineStaged event number 1 carries
      | field         | value     |
      | DispositionID | disp-0002 |

  Scenario: An operator may restock a grade C line the policy would liquidate
    When line 3 is inspected with grade C and qty 1
    And the disposition of line 3 is overridden to RESTOCK with reason customer re-tested the unit as new
    Then the disposition of line 3 is RESTOCK
    And the disposition of line 3 keeps grade C under policy version 1
    And the published disposition events are DispositionDecided,DispositionDecided,ReturnLineStaged

  Scenario Outline: An override that does not explain itself or names no outcome changes nothing
    Given time advances 5 minutes
    And line 1 is inspected with grade A and qty 1
    When a POST request is sent to "/dispositions/disp-0001/overrides" with key "ovr-bad-<case>" and the JSON body <body>
    Then the response status is 400
    And the response problem is "<slug>"
    And the disposition disp-0001 is still RESTOCK
    And the published disposition events are DispositionDecided,ReturnLineStaged

    Examples:
      | case          | slug                     | body                                  |
      | empty-reason  | override-reason-required | {"outcome":"RTV","reason":""}         |
      | no-reason     | override-reason-required | {"outcome":"RTV"}                     |
      | unknown-outcome | invalid-policy         | {"outcome":"SHRED","reason":"recall"} |
