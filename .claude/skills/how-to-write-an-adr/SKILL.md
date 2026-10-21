---
name: how-to-write-an-adr
description: Write an Architecture Decision Record in this repo's numbering and format, including companion ADRs for cross-repo changes. Use when a design decision should be recorded or a change contradicts an existing ADR.
---

<!-- TEMPLATE NOTE (warehouse-harness-template v2): adapt every repo-specific example in this file (file paths, type names, field names) to THIS repo real code. Do not copy-paste verbatim. -->

# How to write an ADR

Use when a change is architecturally significant — a new bounded-context
integration, a reversal of a prior decision, a cross-repo contract change,
or anything a future reader would otherwise have to reverse-engineer from
the diff. Not every change needs one: a bug fix or a routine feature
addition inside an already-decided architecture doesn't.

## Numbering and location

`docs/adr/NNNN-kebab-case-title.md`, four-digit zero-padded,
sequential — check the highest existing number
(`git ls-tree --name-only origin/develop -- docs/adr/` and pick the
next integer, never reuse or guess). ADRs in this repo are PLAIN
markdown: `# ADR NNNN: <title>` heading, then `## Status` with
`Accepted (YYYY-MM-DD).`, then Nygard sections — no frontmatter, no
docs-site build (see `docs/adr/0002-aggregates-and-policy.md` for the
exact shape).

## Format: Michael Nygard's template

```markdown
# NNNN. Title (a short noun phrase)

## Status
Accepted | Proposed | Deprecated | Superseded by ADR-XXXX

## Context
The forces at play — technical, business, constraints — that make this
decision necessary. Write in the past tense, as if explaining to someone
who wasn't there. State the alternatives seriously considered, not just
the one chosen; a reader six months from now needs to know a simpler
option was weighed and rejected, not assume nobody thought of it.

## Decision
What was actually decided, stated as an active, present-tense
declaration ("we will...", not "we might..."). Be specific about the
mechanism, not just the intent — this section should let a reader
implement the same decision from scratch without asking follow-up
questions.

## Consequences
What becomes easier, what becomes harder, and what future work this
creates or forecloses. Be honest about the downsides — an ADR that only
lists benefits reads as marketing, not a decision record.
```

The `## Decision` section is the part worth the most editing effort: see
ADR 0003 (`docs/adr/0003-local-copies-and-consumed-contracts.md`)
for a model example — it states the exact mechanism (one local-copy row
per key, apply only newer versions where a version exists, dedupe on the
CloudEvents id, commit the offset only after the effect), and is specific
enough that the consumer-group guidance in
"how-to-add-an-integration-event" can point straight at it.

## Superseding an earlier ADR

Don't edit the old ADR's Decision section. Add a `## Status` line noting
`Superseded by ADR-XXXX` on the OLD one (a one-line patch — this repo's
convention is to amend with one line, never rewrite an old ADR body), and
open the
new ADR referencing it: `**Accepted.** <date>. Supersedes [NN. Old title](./NNNN-old-slug.md).`

## Cross-repo decisions: use a companion ADR, not one repo's private opinion

When a decision genuinely spans two bounded-context repos (e.g.
facility-layout's functional-location roles enabling wes-work-planning's
travel-graph feature), write ONE ADR per repo, each referencing the other
explicitly as "the companion ADR" with a one-line description of the
split of responsibility — see facility-layout's ADR-0016/0017 pair. Don't
write the decision once in one repo and expect the other repo's readers
to find it; each bounded context's docs site is read independently.

## After writing: verify the gates that actually read ADRs

This repo has no docs-site build — ADRs are plain markdown. What reads
them instead:

- `make check` runs `TestEventCatalogueMatchesContract`
  (`internal/architecture/catalogue_fitness_test.go`), which fails when a
  type in `apis/asyncapi.yaml` has no catalogue line in ADR 0004 — a new
  produced or consumed type with no ADR line fails CI.
- `make arch-test` runs the hexagonal fitness suite, which encodes
  decisions ADR 0002-0004 record.

Run both before opening the PR.
