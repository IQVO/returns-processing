# reports.feature — the ADR 0007 analytics read side (wave 2): the events the
# journeys publish are folded into the analytical store (the same fold the
# inbound analytics consumer applies), and the reports binary's router
# answers the mix, value-recovery, cycle-time and freshness queries over it.
Feature: The returns analytics reports

  Background:
    Given the clock is 2026-10-21T09:00:00Z
    And a policy is declared with window 30 days and cap 5 units per line
    And order ord-7 line 2 is allocated sku SKU-1001
    And order ord-7 line 2 shipped in package pkg-1029
    And a return of 3 units of order ord-7 line 2 for reason DAMAGED is requested at site SITE1
    And a receipt is opened at site SITE1 with lines
      | line | sku      | qty | rma      |
      | 1    | SKU-1001 | 2   | rma-0001 |
      | 2    | SKU-1001 | 1   | rma-0001 |

  Scenario: The disposition mix counts the decisions by outcome
    Given line 1 is inspected with grade A and qty 2
    And line 2 is inspected with grade C and qty 1
    When the published events are projected to the analytics store
    And a reports GET request is sent to "/reports/disposition-mix?from=2026-10-21T00:00:00Z&to=2026-10-22T00:00:00Z"
    Then the response status is 200
    And the response list "rows" has 2 items
    And the response field "rows.0.policy_version" is 1
    And the response field "rows.0.outcome" is LIQUIDATE
    And the response field "rows.0.lines" is 1
    And the response field "rows.0.units" is 1
    And the response field "rows.1.outcome" is RESTOCK
    And the response field "rows.1.lines" is 1
    And the response field "rows.1.units" is 2

  Scenario: The value recovery report counts the restocked units per site and day
    Given line 1 is inspected with grade A and qty 2
    And line 2 is inspected with grade C and qty 1
    When the published events are projected to the analytics store
    And a reports GET request is sent to "/reports/value-recovery?from=2026-10-21T00:00:00Z&to=2026-10-23T00:00:00Z&granularity=day"
    Then the response status is 200
    And the response field "granularity" is day
    And the response list "days" has 2 items
    And the response field "days.0.sites.0.site_id" is SITE1
    And the response field "days.0.sites.0.restocked_lines" is 1
    And the response field "days.0.sites.0.restocked_units" is 2

  Scenario: The cycle time report measures receipt to staged
    Given line 1 is inspected with grade A and qty 2
    When the published events are projected to the analytics store
    And a reports GET request is sent to "/reports/return-cycle-time?from=2026-10-21T00:00:00Z&to=2026-10-22T00:00:00Z&granularity=day"
    Then the response status is 200
    And the response list "days" has 1 items
    And the response field "days.0.staged_lines" is 1
    And the response field "days.0.cycle_time.count" is 1

  Scenario: The freshness report reads the newest applied event; empty until the first
    When a reports GET request is sent to "/reports/freshness"
    Then the response status is 200
    And the response field "as_of" is null
    And the response field "lag_seconds" is null
    Given line 1 is inspected with grade A and qty 2
    When the published events are projected to the analytics store
    And a reports GET request is sent to "/reports/freshness"
    Then the response status is 200
    And the response field "as_of" is 2026-10-21T09:00:00Z
    And the response field "lag_seconds" is 0

  Scenario: A bad range or granularity is a typed 400
    When a reports GET request is sent to "/reports/disposition-mix?from=not-a-date&to=2026-10-22T00:00:00Z"
    Then the response status is 400
    And the response problem is "invalid-report-range"
    When a reports GET request is sent to "/reports/disposition-mix?from=2026-10-22T00:00:00Z&to=2026-10-21T00:00:00Z"
    Then the response status is 400
    And the response problem is "invalid-report-range"
    When a reports GET request is sent to "/reports/value-recovery?from=2026-10-21T00:00:00Z&to=2026-10-22T00:00:00Z&granularity=week"
    Then the response status is 400
    And the response problem is "invalid-report-granularity"
