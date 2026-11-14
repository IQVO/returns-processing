# policy_rules.feature — wave 3: DeclareDispositionPolicy (use-cases.md #8,
# aggregate-design-canvas.md "DispositionPolicy", ADR 0002 "Shape", openapi
# PUT /disposition-policies/{version}): the shape bounds at their boundaries,
# the totality rule, the defaults of an omitted window and veto outcome, the
# declared version becoming current whatever its number, and append-only
# versions. A refused declaration changes nothing.
Feature: Declare the disposition policy (documentation-derived)

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line

  Scenario Outline: An invalid policy is refused with its slug and the current policy stays
    When a PUT request is sent to "/disposition-policies/2" with key "pol-bad-<case>" and the JSON body <body>
    Then the response status is 400
    And the response problem is "<slug>"
    And the current policy is version 1 with window 30 days and cap 5 units per line

    Examples:
      | case          | slug           | body                                                                                                                                                                                                                                                                                  |
      | cap-zero      | invalid-policy | {"returnWindowDays":30,"maxUnitsPerLine":0,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}                |
      | window-below  | invalid-policy | {"returnWindowDays":-1,"maxUnitsPerLine":5,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}                |
      | duplicate     | invalid-policy | {"returnWindowDays":30,"maxUnitsPerLine":5,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"A","productClass":"*","outcome":"RTV"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]} |
      | no-scrap-star | invalid-policy | {"returnWindowDays":30,"maxUnitsPerLine":5,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"Hazmat","outcome":"DISPOSE"}]}  |
      | veto-unknown  | invalid-policy | {"returnWindowDays":30,"maxUnitsPerLine":5,"vetoOutcome":"SHRED","rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]} |

  Scenario: Any positive version is accepted, defaults apply, and it becomes the current policy
    Given order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    When a PUT request is sent to "/disposition-policies/5" with key "pol-5" and the JSON body {"maxUnitsPerLine":1,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}
    Then the response status is 201
    And the response field "version" is 5
    And the response field "returnWindowDays" is 0
    And the response field "vetoOutcome" is DISPOSE
    When a GET request is sent to "/disposition-policies/current"
    Then the response field "version" is 5
    And the response field "maxUnitsPerLine" is 1
    When a return of 2 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the command fails with ErrQuantityExceedsPolicy
    When a return of 1 unit of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    Then the rma status is AUTHORIZED
    And the rma window ends at 2026-10-21T09:00:00Z

  # Spec: ADR 0002, aggregate-design-canvas.md and openapi all say the policy
  # is APPEND-ONLY — "PUT declares version N once", "a change is a new
  # version, never a rewrite". The memory double and the Postgres adapter
  # both UPSERT a re-declared version (ON CONFLICT (version) DO UPDATE), so
  # every decision that recorded policy_version 1 silently points at a
  # different table. The docs do not name the refusal's status or slug, so
  # only the invariant is pinned: the table of version 1 never changes.
  @bdd @known-bug
  Scenario: A declared policy version is never rewritten
    When a PUT request is sent to "/disposition-policies/1" with key "pol-rewrite" and the JSON body {"returnWindowDays":30,"maxUnitsPerLine":9,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}
    And a GET request is sent to "/disposition-policies/current"
    Then the response field "maxUnitsPerLine" is 5
