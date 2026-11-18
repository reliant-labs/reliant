# Forge fast pre-merge tests

Task: `cfc35425-db9a-4395-ad20-394f714f75a8` (2026-10-23).
Scope: test execution, not a promise that every CI check finishes in five minutes.

## Baselines and provenance

Initial investigation used the shared checkout (local main before fetching).
The requested remote-main continuation uses an isolated checkout at
`~/src/rl-agent-worktrees/forge-fast-test-gate`, based on `79c695676b1e`.
Remote main already removed race instrumentation from premerge changed tests;
that optimization is **not attributed to this change**. It also added full
whole-repo guards after the short shards; those remained on the critical path.

Hardware: macOS arm64, Go 1.27.2, 16 logical CPUs, 128 GiB RAM. Other agents were
running heavy work concurrently. At 21:12 UTC load averages were
57.01 / 56.64 / 30.70; at 21:19 UTC 26.67 / 33.71 / 28.44.
The shared Go build/module cache was retained. **Every timing labelled uncached
below disables test-result replay with `-count=1`; none proves cold compilation
or GitHub runner/setup/queue performance.** No private cache or cache wipe.

| Command / scope                                                                                                                                                                                                                                                                                                                                |                       Wall seconds | Result                                                                                                                                                                            |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------: | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Initial `task test:short -- -count=1 -json`                                                                                                                                                                                                                                                                                                    |                               1.21 | Root package only; discarded as a whole-tier measurement (explicit package arguments are required)                                                                                |
| Initial `task test:short -- -count=1 -json ./internal/cli ./internal/generator ./internal/ciguard`                                                                                                                                                                                                                                             |                             131.72 | Existing ForgePinSkew failures plus deliberate new red regressions; generator 51.392s                                                                                             |
| Initial two `scripts/ci-test-shard.sh I 2`, launched together with `GOFLAGS=-count=1`                                                                                                                                                                                                                                                          |      163 aggregate; 162.45 / 96.77 | Existing ForgePinSkew and KCL example failures; not a clean baseline                                                                                                              |
| Remote-main scoped short after gating, same three explicit packages, `-count=1`                                                                                                                                                                                                                                                                |                             245.13 | CLI passes (141.276s), generator fails existing collision test (134.327s), ciguard passes (11.961s); new checkout + heavy concurrent compile, not a comparable speedup experiment |
| Remote-main two final shards together, `GOFLAGS=-count=1`                                                                                                                                                                                                                                                                                      | **195 aggregate; 195.05 / 101.65** | Shard 1 passes; shard 0 only existing generator collision failure; 183 packages covered                                                                                           |
| Full/race retained advisory assertions + CI regressions (`task test -- ./internal/ciguard ./internal/cli -run 'TestChangedTests\|TestWholeRepoGuards\|TestRenderBacked\|TestAdvisoryReport\|TestRunUpgrade_AdoptsANamedScaffoldOnceFile\|TestResolveForceSelection_AcceptsScaffoldOncePaths\|TestShortTier\|TestGateNeeds\|TestRequiredGate'`) |                             212.26 | PASS; CLI 64.222s, ciguard 12.299s; includes compile/link                                                                                                                         |
| Full/race release fixtures + release/shard regressions (`task test -- ./internal/cli ./internal/ciguard -run 'TestReleaseForgeScript\|TestReleaseRequiresExactFullGreen\|TestReleaseChecksFullTestsBeforeEditing\|TestShardReports'`)                                                                                                          |                              65.88 | PASS; CLI 29.271s, ciguard 6.189s                                                                                                                                                 |
| `task test:short -- ./internal/ciguard -count=3`                                                                                                                                                                                                                                                                                               |                              13.26 | PASS, 12.945s package runtime; forced three fresh executions                                                                                                                      |
| Final `task test -- ./internal/ciguard`                                                                                                                                                                                                                                                                                                        |                              28.85 | PASS, race enabled, 28.360s package runtime                                                                                                                                       |

Raw measurement files during this run: `/tmp/forge-fast-scoped-before.*`,
`/tmp/forge-shard-{0,1}.{json,time,log}`, `/tmp/forge-isolated-after.*`,
`/tmp/forge-final-shard-{0,1}.{json,time,log}`, `/tmp/forge-full-scoped.*`,
`/tmp/forge-release-full-fixed.*`. This report is the durable record; do not delete it.

## Design and critical path

Before: three independent PR test entries, but each did serial full guard work:

```
max(setup0 + short0 + deadcodeguard,
    setup1 + short1 + tierguard,
    setup2 + removalguard + writeguard + serial changed-package tests)
```

After:

```
max(setup0 + short0, setup1 + short1, setup2 + changed-package tests)
```

Keep three entries rather than adding flake work after the already-slowest shard.
The changed-test runner now sends the union of selected test names and packages
to **one** `go test` invocation. Go's bounded package scheduler owns concurrency,
shares dependency compilation, and reports all package failures. A same-named
test in another selected package can run additionally; no selected test is lost.
Count-three repetition remains on PRs. Optional `full` mode adds race detection,
and main/manual full runs exercise it as well as the existing full race suites.

The initial workflow's own historical measurements put tierguard at 96s alone,
all guards at 230s with `-p 3` (278s at `-p 1`), before setup. Adding even 96s
serially to the measured 195s shard leaves virtually no five-minute setup
headroom. More shards do not solve whole-program guard contention or org runner
queues. Shortening timeouts only hides results. Whole-program scans and native
KCL renders therefore belong to full/main, while cheap guard assertions still
compile and execute under `-short` in the total package partition.

The advisory CLI fixture rendered real environments through native KCL repeatedly
under `-short` (initial individual tests 2.1–3.1s, plus many renders). The fixture
helper now skips before construction under short mode; all original assertions
remain in full mode and were run under race above. Generator classification unit
tests are unchanged. No blanket suppression of failing assertions.

Shard reporting now records command wall time, not merely body elapsed, in console
and job summary, with an observable warning over 240 seconds (setup headroom).
Per-binary hang timeouts are unchanged. The measured 195s short critical path
leaves 105s for setup and independent repetitions, but **a green under-five-minute
GitHub run is not proven by these local results**, especially with a failing
unrelated test and no cold-cache runner benchmark.

## Coverage and release safety

- `scripts/ci-test-packages.sh check`: PASS, 178 unit + 1 CLI + 4 guards = 183.
- Full guard scans/renders stay in `test-short-warm` on every main push/manual
  dispatch, at `-p 1 -count=1`, derived from the authoritative guard set.
- Full unit and CLI race jobs, tagged integration, e2e/nightly and native Windows
  lanes are retained. No suite or assertion was deleted.
- The release script previously had **no full-green CI barrier**. GitHub release
  preparation now fails closed before editing/tagging/pushing unless the newest
  full `ci.yml` run for the exact source HEAD is completed successfully AND the
  actual full `Test`, `Test (internal/cli)`, and
  `Test (-short whole tier, then guards)` jobs all succeeded. This checks jobs,
  not just `CI gate`, which can waive known main reds. PR/merge-queue greens,
  missing/running/failed/stale runs and API failures are rejected. Local fixture
  remotes have no hosted publication; GitHub publication requires authenticated
  `gh`. No real release was invoked.

## Regressions and validation

Observed red before fix: scheduler fixture expected one invocation but got two;
unknown mode silently accepted; native advisory fixture lacked a short gate;
workflow still put full guards on PRs; release had no before-edit full-test check.
All corresponding regression tests pass after the changes.

The shell fixtures copy scripts opened by the Go test process before executing
fake git/go/gh commands, so they neither touch the real checkout nor hide script
changes from Go's test cache. Independent fixtures run in parallel.

`bash -n`, shellcheck over the four owned scripts, actionlint syntax validation
(with shellcheck disabled), and `git diff --check` pass. Full actionlint reports
four pre-existing SC2046 findings in package-set command substitutions (unit,
CLI, guards, Windows); these intentional splits are unrelated to this change.

Contributor install: `task install:dev`, then `forge --version` reported
`v0.1.47-0.20261023205956-79c695676b1e+dirty`.

## Actual GitHub PR measurements

Run `40026398519`, head `2e00a7dd6`: all three test entries **PASS**.
From workflow creation at 21:23:22 UTC to last test completion at 21:27:10:
**228 seconds including queue and setup**. Short shard jobs took 165s and
118s (test commands 122s and 64s); independent flake job took 85s, with 2s
in its test step. These runner timings may include valid cached results and
are reported separately from the forced-execution local timings above.
The non-test Checks job failed only because prettier reformatted this report's
table; that formatting is corrected in the follow-up commit, not bypassed.

## Existing unrelated failures

Initial checkout: `TestForgePinSkew_*` refusal/warning expectations failed;
`TestKCLModule_ExampleRendersOneOutput` reported KCL `attribute 'Bundle' not found`.
Neither recurred in the final remote-main shards.

Remote main: `internal/generator.TestEnsureCoreComponentsRefusesCaseCollisions`
fails with `EnsureCoreComponentsIn err = <nil>, want a refusal naming button.tsx
and Button.tsx`. Reproduces alone in 1.295s. The test and its implementation were
not changed here. This is the **only failure** in the final two-shard run, so the
full short lane is not claimed green. Relevant CI/release/advisory tests pass.
