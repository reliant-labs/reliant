---
name: ci/lint-rollout
description: Why a new or tightened `forge lint` rule is a warning for one release before it gates - introduced_in / gates_from, how your forge.yaml forge_version decides, the "new in vX; gates from vY" note and the verdict line that counts it, --json's grace object, lint.rules to gate early or extend the grace period, and how a forge author declares a new rule's rollout. Use when a lint finding says "gates from", before bumping the forge pin, or when adding a lint rule to forge.
---

# New lint rules warn before they gate

A forge release that adds a lint rule or lane, or tightens one, must not turn
your gate red the day you pin it. Every rule lint can grade declares two
versions in forge (`internal/cli/lint/lint_rollout_table.go`): `introduced_in`,
the release that shipped it, and `gates_from`, the release it starts to fail
the gate in (always a later one). The `forge_version` in your `forge.yaml`
decides:

| Your pin vs the rule             | A finding that would have failed the gate        |
| -------------------------------- | ------------------------------------------------ |
| below `gates_from`               | a **warning**, with a note; the gate stays green |
| `gates_from` or later            | an error, as before                              |
| no pin / `0.0.0` / not a version | an error (nothing says you are behind)           |

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

Adding a rule to forge? Add it to `gradableRules` and to the rollout table with
`introduced_in` = the release it ships in and `gates_from` = the one after;
`TestEveryGradableRuleDeclaresItsRollout` fails until you do.
