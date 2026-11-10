# PR #2512 repair evidence

## Provenance

This repair starts at the existing remote PR head
`8fb68f0170e652ab99d1c7fc115b32ac3d042f60`, including its B1 regression
fixture. It does not read, modify, remove or supersede any archived worktree's
unpublished work. No duplicate upgrade PR is involved.

Recorded target: merged Forge main
`fb7f0349283ffe3fdcfe9a5735f436fe5e1507bb`, version
`v1.3.1-0.20261110153027-fb7f0349283f`. Verified merge ancestry:

- #2022: `6f5f3326b93034ae54831b02bd1e9ecbbafb7a89`
- #2013: `9816477530bf884f31b1234857520d63cb0c969a`
- #1997: `50e2684dd3a22f24f93210aee87f069982776c9f`
- #2000: `25d82dfda4481503bb29913da4c26dc6ea7b280b`

## RC-09: newly gating lint transitions

Compared the **original consumer pin** `9caded4a37d5b0fc2c5c9d86a89900175d4af648`
to the recorded target, not merely the intermediate PR pin `de8e6d23783c`.
All evidence here is from the public Forge repository.

`internal/cli/lint/lint_rollout_table.go` is byte-identical at both commits
(blob `eee93b458d20dba58da09a55dbf73f566313a22b`). The grading implementation
`internal/cli/lint/lint_rollout.go` is also identical
(blob `5e09d89716bd5492ca60f96f56ae81b1735df7a6`).
The complete table has exactly one `GatesFrom` threshold crossed:

| Rule | Introduced | Gates from | Previous pin | Target |
|---|---|---|---|---|
| `forge-identity-subject-unreadable` | v1.2.0 | v1.2.1 | v1.2.1-0.… sorts **below** v1.2.1: warn | v1.3.1-0.… sorts above v1.2.1: gate |

All other declared thresholds are v1.1.1 or earlier and already gated at
the previous pin; none is newly added. `comparablePin` / `applyRollout` use
Go semver comparison, preserving pseudo-version ordering. Explicit
`lint.rules` overrides are unchanged by this PR.

Also inspected every changed file under `internal/cli/lint` in that range:
closed-set changes improve the two-transaction widening fix hint;
hosted-image changes document the third-party exception; workload drift
stops reporting Go orphans in non-Go projects; `GradableRules` only exports
the existing registry to guards. The changed slow-test analyzer under
`internal/linter/slowtest` excludes commands never started and bounded
sub-millisecond probes and recognizes testing helpers; it does not add a
new gating rule. No newly tightened rollout was hidden by comparing table
rows alone.

Reproduce the central comparison in a Forge checkout:

```sh
git diff 9caded4a37d5b0fc2c5c9d86a89900175d4af648..fb7f0349283ffe3fdcfe9a5735f436fe5e1507bb -- internal/cli/lint/lint_rollout_table.go internal/cli/lint/lint_rollout.go
git diff 9caded4a37d5b0fc2c5c9d86a89900175d4af648..fb7f0349283ffe3fdcfe9a5735f436fe5e1507bb -- internal/cli/lint internal/linter/slowtest
```

This is source/threshold evidence, not a claim that full-project lint passed.
The author runs only scoped lanes; full verification belongs to the steward.

## Verification

- Target upgrade check and upgrade converged, with `--skip lint` to avoid
  a whole-repository author gate; `project upgrade list`: up to date.
- Canonical pin script: `GOWORK=off go build ./cmd/reliant`, then pinned
  generation and `task generate:docs` passed.
- `lint --conventions --scope cmd/reliant/cmd --scope deploy/kcl/demo --quiet`:
  one gating linter passed.
- `lint --column-markers --scope db/migrations --quiet`: passed, including
  the newly gating identity-subject rule.
- Target `kcl eval deploy/kcl/demo/main.k -S output`: frontend
  `runtime_config_spec.OTEL_REPLAY.value` is `"true"`, replay telemetry is
  enabled and environment `observability.policy.opt_in` is `["replay"]`.
- `task test:short -- ./cmd/reliant/cmd/... ./internal/app/...`: both passed.
- Non-short `go test -run '^TestPinForgeBuildContract$' -v ./cmd/reliant/cmd`:
  default, separate scoped arguments, `GOWORK=off`, and failed-build
  generation stop passed. No full gate or whole-project vet was run.

The trusted modal/sidebar adoption note explicitly allows a follow-up.
This pin repair preserves the existing customized components rather than
silently replacing their local focus behavior; it makes no claim that those
components were reinstalled or their frontend consumers retested here.
