---
name: ci/lint-rollout
description: The project's lint dials — `lint.lanes` to switch a whole lane off, `lint.rules` for one rule's severity, `lint.allow_skip` for a lane that could not run — and why a new or tightened `forge lint` rule is a warning for one release before it gates - introduced_in / gates_from, how your forge.yaml forge_version decides, the "new in vX; gates from vY" note and the verdict line that counts it, --json's grace object, lint.rules to gate early or extend the grace period, and how a forge author declares a new rule's rollout. Use when a lint finding says "gates from", before bumping the forge pin, or when adding a lint rule to forge.
---

# New lint rules warn before they gate

Every rule lint can grade declares the release that introduced it
(`introduced_in`). A rule with a warning window also has a `gates_from` date,
the later release where it starts to fail the gate. For rules with that window,
the `forge_version` in your `forge.yaml` decides:

| Your pin vs the rule             | A finding that would have failed the gate        |
| -------------------------------- | ------------------------------------------------ |
| below `gates_from`               | a **warning**, with a note; the gate stays green |
| `gates_from` or later            | an error, as before                              |
| no pin / `0.0.0` / not a version | an error (nothing says you are behind)           |

A pseudo-version pin (`v1.5.1-0.20261113062323-685069ed7517`) is read as the
release it leads to (`v1.5.1`), so a commit pinned by pseudo-version and the
same commit pinned by its tag get the same verdict.

A pin that jumps over a rule's whole window (`v1.0.0` → `v1.7.0`, past a rule
introduced in `v1.5.0` that gates from `v1.5.1`) does not make that rule gate
on arrival: it warns for the whole of the new pin and gates from the next one.
forge reads where you came from out of git history — the `forge_version`
forge.yaml held before its newest change, or HEAD's when the bump is not
committed yet — so nothing is written for it. Without that history (no git, a
`--depth 1` clone, which is `actions/checkout`'s default) the pin alone
decides, as before: give the job that runs `forge lint` `fetch-depth: 0` (or
at least enough depth to reach the pin bump) for it to grade the same way
your checkout does.

Its note reads `is new in v1.5.0, after this project's previous forge pin
v1.0.0, so it warns at v1.7.0 and gates from the next pin`, and `--json` adds
`previous_pin` and `gates_after` to the grace object.

`forge project upgrade` lists the rules whose grace the pin bump ends under
"Starts gating" (`starts_gating` in `--json`), with how many of their findings
now gate, so bump the pin with it rather than by hand.

The warning says what to do:

```
⚠️  closed-sets lint — 2 warnings
   ⚠ db/migrations/0001_x.up.sql:4 [forge-closed-set] CHECK lists a value Go does not grant
      → warn: [forge-closed-set] is new in v1.0.0 and gates from v1.0.1; gate now with lint.rules: {forge-closed-set: error}
✅ All 3 gating linters passed; 2 new-rule findings warn until they gate (forge-closed-set ×2, gates from v1.0.1)
```

The last line is the verdict, and it counts them, so a green run never hides
that a pin bump will turn red. `forge lint --json` puts the same facts on the
finding as `"grace": {"introduced_in", "gates_from", "fix"}`, and `fix` names
the `forge fix` codemod when one exists.

`lint.rules` always wins over the rollout, in both directions: `{rule: error}`
opts a project in to gating early (do this once you have cleaned the findings,
so a regression cannot slip back in during the grace period), `{rule: warn}`
extends the grace period for as long as you need, and `{rule: off}` drops the
rule. The golangci-lint passes over files behind your build tags roll out the
same way, under the key `golangci-build-tags`.

Adding a rule to forge? Add it to `gradableRules` and declare its introduction
release in the rollout table: `NEXT_VERSION` for a rule no tag carries yet.
A warning window is opt-in: `Grace` names the minor after the introduction
release as the first release that gates, and records why the rule needs grace.
A rule whose commit missed the tag its row names ships in the next release,
so its row moves to `NEXT_VERSION` and it warns through that release (owner,
forge#2638). The cohort first declared as v1.9.0 missed that tag, so it ships
in v1.10.0 and gates from v1.11.0; `crud-table-unowned`, declared v1.10.0,
missed that tag, so it ships in v1.11.0 and gates from v1.12.0. Rules whose
windows were closed in #2410 stay closed. `TestRolloutRowsAreWellFormed`
checks these restrictions, and `TestGraceRowsNameAReleaseThatCarriesTheirRule`
proves each released row's tag carries its rule.

## Switching a whole lane off: `lint.lanes`

A repository adopting forge often lints its frontend with its own toolchain,
migrates its schema with another tool, or has no database grants for forge to
check. Switch those lanes off once, in forge.yaml:

```yaml
lint:
  lanes:
    frontend: off # frontend lint, typecheck and lockfile
    migration-safety: off
    db-grants: off
```

A key is a lane's flag spelling with or without `-lint` (`migration-safety`,
`db-grants-lint`), its verdict name, `format`, or the `frontend` alias; an
unknown one fails `forge lint` with the list. An off lane never runs and is
never silently missing: the report prints `○ frontend lint — off (forge.yaml
lint.lanes)`, the verdict counts it (`✅ 12 gating linter(s) passed; 3 off
(forge.yaml lint.lanes): …`), and `--json` carries it as a `skipped` finding.
Naming the lane on the command line (`forge lint --migration-safety`) still runs
it. The three dials, coarsest first: `lint.lanes` (a lane is not this project's
concern), `lint.rules` (one rule's severity), `lint.allow_skip` (a lane that
could not run here, accepted with a reason).
