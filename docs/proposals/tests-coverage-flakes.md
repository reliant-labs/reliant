# Proposal: Tests — coverage, results and flaky tests as a forge default

**Status:** proposal. The owner settled all five decisions and the two
smaller calls on 2026-11-20 ([Decisions](#decisions-owner-2026-11-20)).
Decisions 3 (coverage source) and 5 (retention) were reopened and then decided
on the research in [Amendment 1](#amendment-1-coverage-source-and-retention-2026-11-20).
Where the body below disagrees with those two, the decisions win. Phase 0
measured the design on 2026-11-20/21, and the eight amendments that came out
of it are folded into the body
([Amendment 2](#amendment-2-phase-0-results-2026-11-21)). Phase 1 is in
progress; nothing is merged in forge, control-plane or reliant yet. The
implementation is tracked in
[forge#2628](https://github.com/reliant-labs/forge/issues/2628).
**Date:** 2026-11-20, updated 2026-11-21
**Scope:** forge (test capture, coverage merge, instrumented builds, KCL, CLI,
scaffold and upgrade playbook), control-plane (test-run store, merge, flake
scoring, API), reliant (the Tests pages, two agent workflows), plus moving
forge, control-plane and reliant onto it.

## Summary

Every forge project records its test results and coverage by default and keeps
them, per commit and per release. Coverage from unit tests, integration tests
and end-to-end runs against coverage-instrumented servers is merged into one
report. That report gives a percentage per lane, a combined percentage, views
by package and by file, and coverage of a commit's changed lines against its
base. Flaky tests are detected from the record itself, and each gets a score
and a state. A suggested quarantine and a linked issue come with it, plus a
button that gives the fix to an agent. The record lives in the control plane
and is shown in a **Tests** section of the Reliant project shell. It is
evidence attached to a release, the same way the SBOM is.

The owner settled four things on 2026-11-20, and this document does not reopen
them:

- **On by default.** Every project forge scaffolds records unit test results
  and unit coverage. Preview and dev envs run coverage-instrumented servers, so
  integration and e2e runs add their coverage too. Existing projects get the
  same through a `forge project upgrade` playbook.
- **Report-only by default.** No default can block a deploy or a merge.
  Anything that can refuse something is opt-in, defaults to `audit`, and goes
  through the warn-first rollout.
- **Dogfood.** Once it is built, control-plane, reliant and forge move onto it.
  Each repo's move is its own tracked task ([section 6](#6-dogfood-forge-control-plane-reliant)).
- **One flake system.** forge's own flake gate (changed tests run three times),
  `scripts/flake-report.sh` and the `flake:suspected` / `flake:removed` issue
  flow are ported onto the product. They are not kept as a second system.

The recommended design:

1. **The project's own Taskfile lanes produce the evidence.** Each scaffolded
   lane (`test`, `test:integration`, `test:e2e`, `test:frontend`) runs through
   `forge test capture`, which execs the lane's command unchanged. Capture
   stores the `go test -json` stream (or a JUnit or Vitest report), plus the
   lane's coverage, under `.forge/tests/runs/<run>/`. It renders the stream as
   readable output and exits with the command's own exit code. forge never runs
   `go test` with flags of its own
   ([1.2](#12-producing-it-the-projects-own-lanes)).
2. **Coverage-instrumented builds are a KCL knob,** `tests = forge.Tests
{coverage = …}`. Its default is computed: on for `lifecycle = "local"` and
   `"ephemeral"` envs and for previews, off for persistent envs. Turning it on
   for a persistent env is a render error unless it is explicitly allowed. An
   instrumented build is a `cover` variant of the release, so it can never be
   promoted to a persistent env. Its coverage is pulled from the running
   process through serverkit's localhost debug listener, and written on exit
   as a fallback
   ([1.3](#13-instrumented-builds-integration-and-e2e-coverage)).
3. **One merge function, `pkg/testreport`,** is used both by the forge CLI and
   by the control plane (which pins forge), so local and hosted numbers agree.
   Lanes merge by statement block. A statement counts as covered when any lane
   covered it. Generated code is left out of the percentage by default, because
   forge knows which files it generated ([1.4](#14-merging-and-accumulating-across-lanes)).
4. **Flakes are detected from the record.** There are three signals: a pass
   and a fail of the same test on the same tree, a pass on retry within a run,
   and repeat events on distinct commits of the default branch. Host failures
   are matched by signature first and never score. control-plane's taxonomy,
   already in production (confirmed / real / raced / inconclusive), becomes
   the product's vocabulary, with `host` added and `broad` replaced by
   correlated-failure grouping. So does its quarantine file format,
   `ci/quarantine/<lang>/<pkg>/<Test>.yaml`
   ([1.5](#15-results-and-flaky-tests)).
5. **`reliant forge test`**, a verb group with `--json` documents and the
   shared exit-code table:

   - `report` merges runs and uploads them;
   - `coverage`, `runs` and `flakes` read the record;
   - `collect` pulls coverage from a running env;
   - `ingest` takes JUnit, LCOV, Cobertura or istanbul files from any runner;
   - `quarantine` writes the quarantine file;
   - `run` runs the project's own Taskfile lanes as one grouped run.

   Upload is on by default from CI and off for a local run on a dirty tree
   ([1.7](#17-cli)).

6. **control-plane `TestsService`.** Postgres holds run, case, coverage-summary
   and flake rows. Object storage holds the raw reports and merged coverage
   documents, under an org prefix. A worker does ingest and merging, and the
   scope is `tests:read`. The service advertises `test_features`
   ([section 2](#2-control-plane)).
7. **Reliant web gets a Tests group in the project nav** with four pages:
   Overview, Coverage, Runs and Flaky tests. It also gets a Tests tab on a
   release, a Tests column in the releases list, an Overview card, and "Needs
   attention" items ([section 3](#3-reliant-web)).
8. **Agent tie-in.** "Fix this flaky test" and "Raise coverage of `<pkg>`"
   start as AskReliant chats and then become built-in workflows
   (`fix-flaky-test`, `raise-coverage`). Both follow worktree → agent → verify
   → PR ([section 4](#4-agent-tie-in)).

Every change is additive: new verbs, fields, KCL schemas, RPCs, tables and
workflow files, plus a playbook for existing projects. Nothing changes meaning
([section 7](#7-semver-and-backwards-compatibility)).

## What exists today

| Piece                   | State                                                                                                                                                                                                                                                                                                                                                                                                      | Where                                                                                                                                                  |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Test lanes (scaffolded) | `test` (`go test -race -count=1 ./...`, then `test:frontend`), `test:short`, `test:integration` (`-tags=integration`), `test:e2e` (`-tags=e2e ./e2e/...`). **No lane emits `-json`, coverage or JUnit**                                                                                                                                                                                                    | `internal/templates/project/Taskfile.yml.tmpl:139-268`                                                                                                 |
| Coverage                | One `task coverage`: unit only, `-coverprofile`, then HTML. CI uploads `coverage.out` as a workflow artifact when `ci.test.coverage: true` (default false), and nothing reads it. `forge doctor` checks `go tool covdata` exists                                                                                                                                                                           | `Taskfile.yml.tmpl:428-440`, `internal/config/config.go:1123-1126`, `internal/templates/ci/github/ci.yml.tmpl:253-268`, `internal/doctor/toolchain.go` |
| Instrumented builds     | None. `GoBuild.flags` is passed to `go build` verbatim, and its comment already names `["-cover"] for an e2e env`, but that covers only builds forge runs itself. The Dockerfile and `air.toml` hard-code their `go build` lines                                                                                                                                                                           | `internal/cli/build.go:2270`, `Dockerfile.tmpl:65-67`, `air.toml.tmpl:9`                                                                               |
| `go test -json` readers | `forge ci verify-test-run` (zero-evidence and mass-skip rules, `--gate-json`) and `forge ci test-budget` (text only). The scaffolded `release.yml` feeds `task test -- -json` into `verify-test-run --gate-json gates/test.json`, which travels with the deploy as a gate                                                                                                                                  | `internal/cli/ci_test_run.go`, `internal/testreport/`, `internal/cli/ci_test_budget.go`, `release.yml.tmpl:96-104`                                     |
| `forge test`            | **Removed on purpose:** a second spelling of the suite reports on a different suite than the one the project runs (`ci_test_run.go:28-33`)                                                                                                                                                                                                                                                                 | `internal/cli/ci_test_run.go:28`                                                                                                                       |
| Gate document           | `forge.ci.gate/v1`, with checks and file:line findings. The whole of `task test` is one opaque check, the verdict cache is local and keyed by tree, and no per-test data is kept                                                                                                                                                                                                                           | `internal/cigate/document.go`, `cache.go`, `plan.go:516-531`                                                                                           |
| Flake gate              | forge repo only: `scripts/ci-changed-tests.sh` runs added and edited tests `-count=3`. reliant has `scripts/test-flake.sh` (`task test:flake`), and control-plane's `task ci` reruns changed test packages with `-cpu=1,2,4`. **Nothing is scaffolded for users**                                                                                                                                          | the three repos' `scripts/`                                                                                                                            |
| Flake report            | `scripts/flake-report.sh` in all three repos reruns each failed test once under `-race` and files `flake:suspected`. control-plane adds `--quarantine` (`flake-quarantine.sh`, a `flake:removed` issue, a PR adding `ci/quarantine/go/<pkg>/<Test>.yaml`, which `scripts/lib/quarantine.sh` turns into `go test -skip`) and the verdict taxonomy confirmed / real / raced / inconclusive / unrerun / broad | `control-plane/docs/flake-quarantine.md`, `control-plane/ci/quarantine/`                                                                               |
| Per-commit results      | control-plane's `ci-full-main.sh` records `ci-full/<sha>.json` (steps, failures, pins) as git notes on `refs/notes/ci-full`, which control-plane's release-cut script reads                                                                                                                                                                                                                                | `control-plane/scripts/ci-full-main.sh`                                                                                                                |
| Release evidence        | `release.Artifact.SBOM *SBOMRef` and `Release.SourceSBOM`. Evidence sits outside identity and is backfilled, never replaced                                                                                                                                                                                                                                                                                | `pkg/release/sbom.go`                                                                                                                                  |
| Graceful exit           | Generated servers drain on SIGTERM (`signal.NotifyContext`) and `main` returns normally, so a `-cover` binary writes its counters. `os.Exit(1)` only on error. serverkit runs a pprof listener on `127.0.0.1:6060`, routable from nowhere                                                                                                                                                                  | `cmd-tree-serve-graph.go.tmpl:90`, `cmd-tree-root.go.tmpl:110-118`, `pkg/serverkit/process.go:58-68`                                                   |
| control-plane           | Stores nothing about tests. The SBOM work is the template: proto, migration, `internal/vulnstore`, `internal/adapters/vulnbucket`, a copy-state machine, retention by keep-set, capabilities withheld until live                                                                                                                                                                                           | `control-plane/internal/vulnstore/`, `rpc_get_capabilities.go:68-104`                                                                                  |
| reliant web             | No test concept. The project shell has a Security group (Vulnerabilities, SBOMs). Every project gets the same nav, and empty pages render `AppSectionEmptyState`                                                                                                                                                                                                                                           | `frontends/web/src/components/Layout/sidebar/navConfig/hybrid.ts:38-55` (branch `feat/web-project-shell-proto`)                                        |

So the pieces exist but are scattered. They are one-off reports, scripts that
live in three repos and are copied between them, and a coverage file nothing
reads. This proposal turns them into one record.

## 1. The forge default

### 1.1 What is recorded

A **test run** is one execution of one lane on one tree. It has three kinds of
evidence:

| Evidence                                                                       | Go                                                                                                                                          | Frontend (TS)                                | Any other language    |
| ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------- | --------------------- |
| **Results**, one row per test case: status, duration, attempts, failure output | `go test -json` stream                                                                                                                      | Vitest JSON or JUnit reporter                | JUnit XML             |
| **Coverage**, per statement, kept per file                                     | `-coverprofile` text profile (unit, integration); `GOCOVERDIR` covdata from instrumented binaries, converted with `go tool covdata textfmt` | `@vitest/coverage-v8`, istanbul JSON or LCOV | LCOV or Cobertura XML |
| **Run metadata**                                                               | commit, tree hash, dirty flag, branch, base, lane, command, host, CI provider and run URL, env (for e2e), forge version                     | same                                         | same                  |

The canonical forms are two forge documents:

- `forge.test.results/v1`: cases normalised from any input. Failure output is
  truncated to 8 KiB per case and scrubbed of known secret shapes (the
  patterns control-plane's `scripts/lib/scrub-secrets.sh` uses).
- `forge.test.coverage/v1`: files → statement blocks (start, end, statements,
  hit count), each with its lane. Go blocks map one to one. istanbul
  statements map one to one. LCOV and Cobertura have only lines, so a line is
  treated as one statement and the document says so (`granularity: line`).

The raw input is kept next to each normalised document, so a later parser fix
can re-derive it.

A test is identified by `(language, package, name)`, for example
`go:github.com/acme/shop/internal/billing:TestRefund/partial`. Subtests are
recorded. Flake scoring uses the top-level test, because that is what `-run`
and quarantine address.

### 1.2 Producing it: the project's own lanes

`forge test` was removed because a second spelling of the suite reported on a
different suite. This design keeps that rule: **forge never decides how the
tests run.** The flags are visible in the project's Taskfile, which the
project owns. forge records the run.

The scaffolded lane:

```yaml
test:
  vars:
    RUN: { sh: "{{.FORGE}} test begin" } # reuses $FORGE_TEST_RUN when an outer `forge test run` set it
  cmds:
    - "{{.SLOT}} {{.FORGE}} test capture --run {{.RUN}} --lane unit --
      go test {{.GOTESTRACE}} -count=1 -json -covermode=atomic -coverprofile={{.RUN}}/unit/cover.out -timeout 120s ./..."
    - task: test:frontend
      vars: { RUN: "{{.RUN}}" }
```

- **`forge test begin`** prints a run directory, `.forge/tests/runs/<ulid>/`,
  and writes its `run.json` (commit, tree hash, dirty flag, branch, CI
  environment). When `FORGE_TEST_RUN` is already set, it prints that directory,
  so several lanes join one run.
- **`forge test capture`** execs the command unchanged. It tees the raw stream
  to `<run>/<lane>/results.json`, renders it as ordinary `go test` text
  (gotestsum-style, so `-json` costs the human nothing), records start, end and
  exit code, and **exits with the command's exit code**. It adds no flag. A
  stream it cannot parse is stored as-is and recorded `results: unparsed`. The
  test verdict never changes.
- **Coverage in the unit lane uses `-coverprofile`, not `-test.gocoverdir`.**
  Checked on Go 1.27.4 in all three repos: a `-coverprofile` run replays from
  the test cache with the same blocks and counts, and a run that passes
  `-test.gocoverdir` through `-args` never does. Replay only helps the cached
  `-short` lanes: forge's merge-tier shards, control-plane's `task ci` tier
  and reliant's merge gate, which carry most of the runs. Capturing coverage
  there is close to free (a warm `-coverprofile` run took 4.3s, 2.8s and 5.1s,
  every package cached). A `-count=1` lane such as `task test` never replays,
  with or without coverage.
- **Each package covers its own statements**, which is Go's default.
  `tests.coverage.packages: module` in forge.yaml switches to
  `-coverpkg=<module>/...`, which counts code reached from other packages'
  tests but instruments every package in every test binary. It stays opt-in:
  it cost up to 2.7× the cold instrumented compile (forge: 241s against 88s),
  and its raw profile repeats each block once per test binary that links it,
  18 to 31 times over (105, 228 and 457 MB on a fifth of each repo's
  packages). Capture deduplicates such a profile by block key before anything
  is stored.
- **`-covermode=atomic`, always.** Every scaffolded lane, `GoBuild` and the
  Dockerfile `cover` stage pass it explicitly. Without it a lane that does
  not run `-race` gets `set` mode, which records 0 or 1 instead of a count.
  Phase 0 measured no difference in test time between the two. A `set`
  profile from a project's own lane is still accepted, and is merged as
  covered or not covered ([1.4](#14-merging-and-accumulating-across-lanes)).
- **`test:short` is not captured locally.** It is the inner loop, and it
  stays exactly what it is. `task test` and the CI lanes are what get
  recorded, including a CI lane that runs the `-short` tier.
- **Capture and the test cache.** `forge test capture` exports
  `FORGE_TEST_RUN`, `FORGE_TEST_LANE` and `FORGE_TEST_LANE_DIR` to the lane's
  command, because a nested `forge test begin` and Vitest's coverage
  directory need them. Go's test cache keys on the environment variables a
  test reads, so a project whose tests read those variables loses cache
  replay, since their values change every run. In practice only forge's own
  tests read them, and forge's `scripts/ci-go-test.sh` unsets them before
  starting Go ([forge#2626](https://github.com/reliant-labs/forge/pull/2626),
  which moves forge's own lanes onto capture).
- **Frontend.** The scaffolded `vitest.config.ts` gets `coverage: {provider:
"v8", reporter: ["json", "lcov"], reportsDirectory: process.env.FORGE_TEST_LANE_DIR}`.
  Coverage stays off when the variable is unset, so a bare `npm test` is
  unchanged. `@vitest/coverage-v8` becomes a devDependency, pinned to the same
  major as `vitest` (`frontend_vitest_pin_test.go` holds both).
- **Overhead.** Phase 0 found no measurable test time from instrumentation,
  with or without `-race`, in paired runs on all three repos. The cost is
  compilation: a cover build is a second build-cache variant of every tested
  package, paid once per source change (30 to 150s more for the cold build of
  the sample). Instrumented binaries are about 3% larger and use about 4%
  more memory. `tests.coverage: false` in forge.yaml turns coverage off for a
  project, and `FORGE_TESTS=off` turns capture into a pass-through for one
  invocation.

### 1.3 Instrumented builds: integration and e2e coverage

Integration tests that run in the test process (`-tags=integration`, testkit
starting a service in-process) are covered by `-coverprofile` like unit tests.
**E2e tests run against a running stack.** The coverage that matters there is
the servers' coverage, so the servers must be built with `-cover`.

**KCL.** A new schema follows the `PGO` precedent (`kcl/schema.k:3164`):

```
schema Tests:
    # Build this env's Go workloads with -cover and collect their coverage.
    # Default: on for lifecycle "local"/"ephemeral" and previews, off otherwise.
    coverage?: bool
    # The packages to instrument: "main" (the binary's module, minus gen/), or explicit patterns.
    coverage_packages: "main" | [str] = "main"
    # Required to turn coverage on in a persistent env (one with no lifecycle and no preview).
    coverage_in_persistent_env?: str        # the reason, recorded in the release shape

schema Bundle:
    tests: Tests = Tests {}

# Per binary, as PGO does: GoBuild.coverage?: bool; DockerBuild.coverage?: {str: bool}
```

- **The default is computed, not scaffolded.** `coverage` unset means
  `lifecycle in ("local", "ephemeral") or preview`. The scaffolded dev env is
  `lifecycle = "local"`, and `forge env preview create` copies the template env
  and sets `preview`, so dev and every preview are instrumented with no line in
  `main.k`. An env with neither is persistent. forge already treats such an env
  as production (`schema.k:8560-8577`), so it is never instrumented by default.
- **Persistent envs refuse it.** `coverage = True` on a persistent env fails to
  render unless `coverage_in_persistent_env = "<why>"` is set. This guards a
  `staging` someone deliberately uses for e2e.
- **One binary, one answer.** Two workloads in one env that run the same binary
  must agree on coverage, or the env does not render. This is the PGO rule, and
  it closes the existing "first workload's flags win" dedup gap
  (`build.go:2294`) for this flag.

**Build, per runtime:**

| Runtime                                      | What changes                                                                                                                                                                                                                                                                                                                                          |
| -------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Host (`forge.OnHost`, forge runs `go build`) | `-cover -covermode=atomic -coverpkg=<pkgs>` is appended to the build. The process gets `GOCOVERDIR=.forge/tests/covdata/<env>/<workload>/`                                                                                                                                                                                                            |
| Dev under air                                | The scaffolded `air.toml` builds with `go build $FORGE_GO_BUILD_FLAGS …`, and `forge env up dev` sets the variable. Every restart writes a counter file. Counters are grouped by build fingerprint (the `covmeta` hash), and a report uses the newest build's (older builds are kept and labelled `stale build`, never merged across a source change) |
| Image (Dockerfile)                           | A new `cover` stage beside `debug` (`go build -cover -covermode=atomic`), built with `--target cover`. The prod stage stays byte-identical, so its digest is unchanged. The env var is `GOCOVERDIR=/var/run/forge/covdata` on an `emptyDir`                                                                                                           |
| Prebuilt image, non-Go server                | Not instrumented. Recorded as `coverage: unavailable (not a forge Go build)`. Other languages come later ([phase 6](#phase-6-follow-ups-each-optional))                                                                                                                                                                                               |

**Identity.** A `-cover` build is still reproducible (SOURCE_DATE_EPOCH), but
it is a different digest. It is recorded as a `cover` variant of the same
release (`release.ModeVariant`, `pkg/release/release.go:84-98`). Images become
the first variant users. A deploy refuses to put a `cover` digest on a
persistent env. forge checks this before it writes the promotion, and the
control plane checks again inside `Promote` (capability
`refuses-cover-variant`). Prod therefore always runs the digest staging ran.

**Collection.** There are two paths, and the first one is preferred:

1. **Pull.** serverkit registers `/debug/coverage` on its existing pprof
   listener (`127.0.0.1:6060`, routable from nowhere). The handler calls
   `runtime/coverage.WriteMetaDir` and `WriteCountersDir` into a temp dir and
   returns a tar. `?reset=1` calls `ClearCounters`, which is valid in atomic
   mode, so a run can start from zero. A binary built without `-cover` answers 404. `forge test collect <env>` reaches it with a port-forward (cluster),
   `exec` (compose) or localhost (host). The pod keeps running, and a later
   SIGKILL loses nothing that was already pulled.
2. **Exit flush.** Go writes the counters to `GOCOVERDIR` when `main`
   returns and on `os.Exit`, whatever the code. On SIGTERM the server drains
   and returns from `main`, and a server that fails and calls `os.Exit(1)`
   writes them too (phase 0 saw both). Only a SIGKILL after the grace period,
   an unrecovered panic or a fatal signal loses the counters since the last
   pull, and the report says so. `forge env down` and `forge env preview
delete` run `forge test collect` before teardown, best effort, with a
   warning on failure. Pull stays the preferred path for long-lived pods,
   which may never exit cleanly.

**The e2e lane** wraps the run in a reset and a collect:

```yaml
test:e2e:
  cmds:
    - "{{.FORGE}} test collect {{.ENV}} --reset --run {{.RUN}}" # zero the counters
    - "{{.FORGE}} test capture --run {{.RUN}} --lane e2e -- go test -tags=e2e -json -count=1 -timeout=20m ./e2e/..."
    - "{{.FORGE}} test collect {{.ENV}} --run {{.RUN}} --lane e2e" # pull, convert with covdata textfmt
```

`ENV` defaults to `dev`. In the scaffolded preview workflow it is the preview.

**Frontend e2e coverage** (istanbul through a `forgeCoverage()` plugin in
`@reliantlabs/forge-web-runtime/vite`, with Playwright dumping
`window.__coverage__`) follows the same variant rule. It is a phase 6
follow-up, because forge does not scaffold Playwright today.

### 1.4 Merging and accumulating across lanes

`pkg/testreport` (public, held to v1, imported by control-plane) holds the one
merge function:

- **Unit of coverage: the statement block.** Blocks are keyed by `(file, start
line.col, end line.col)`. That key is the same in a unit profile, an
  integration profile and covdata converted by `textfmt`, so the union is
  exact. Phase 0 checked it against Go's own `covdata merge` on six reliant
  packages: 1,402 covered blocks in each, 82.7% from `go tool cover -func`
  for both. Hit counts sum, which is only meaningful because every input is
  `atomic` or `count`. A `set` profile is merged as boolean: a block it hit
  counts as covered, with no count. Duplicate input (the same report digest)
  is idempotent.
- **Deduplicated by block key.** `textfmt` over covdata from several binaries
  prints each block once per binary, and a `-coverpkg` profile repeats it
  once per test binary. Blocks with the same key become one block, and their
  counts sum.
- **Same source only.** Lanes merge only when their tree hashes are equal.
  Runs on a dirty tree merge only with runs on the same dirty state, and never
  into a commit's report. Mixing them is refused (`exit 2`, "lanes come from
  different trees"), never averaged.
- **Same build only.** Go does not enforce the rule above: phase 0 ran
  `covdata merge` over two builds of one package after a one-line shift, and
  it reported 72 statements for a 36-statement package, at 66.7%, with no
  error. So covdata is grouped by its `covmeta` hash, which fingerprints the
  build, and two builds of one package are never merged. While deduplicating,
  two blocks with one key but different statement counts prove the source
  differs. That is refused (`exit 2`, "coverage from different builds of
  `<pkg>`"), never averaged.
- **The denominator is every instrumented statement.** Since Go 1.22, packages
  without tests appear at 0%, so untested code counts.
- **Generated code is excluded by default.** That is `gen/`, `*_gen.go`,
  `mock_gen.go`, `*.pb.go`, `*-hooks.ts` and anything forge's own ownership
  record marks as generated. It is reported on its own line. It matters most
  for servers: 21% of reliant's API server statements (42.6k of 205k) are
  generated. `tests.coverage.exclude` adds
  globs, and `tests.coverage.include_generated: true` turns the default off.
- **Languages are kept apart.** Go and TypeScript percentages are shown
  separately. A combined project figure is weighted by statements and labelled
  as such, because a Go statement and a TS statement are not the same unit.

**Views** (all computed by `pkg/testreport`, the same locally and hosted):

| View                  | Definition                                                                                                                                                                                                                                                                                                |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Per lane              | covered / instrumented statements, for that lane's runs on the tree                                                                                                                                                                                                                                       |
| Combined              | a statement counts as covered if any lane covered it                                                                                                                                                                                                                                                      |
| Only-e2e              | statements no unit or integration test reaches, only e2e: the "is this tested anywhere but in prod-like traffic?" list                                                                                                                                                                                    |
| Per package, per file | the same numbers, rolled up by Go package (or directory for TS) and per file                                                                                                                                                                                                                              |
| Diff coverage         | of the **executable lines changed** between the base and the commit, the fraction covered. The base is the PR's base sha, or else the merge base with the default branch. forge computes `git diff -U0 base...head` and sends the changed line ranges with the report, because the server has no checkout |
| Delta                 | combined % of the commit minus combined % of the base commit's report. When the base has no report, the delta says "no report for base" and is never computed against an older one                                                                                                                        |

**Accumulation.** A **commit report** is the merge of every run uploaded for
that commit's clean tree: sharded CI jobs, separate lanes, a preview's e2e. It
grows as runs arrive. The commit's `coverage_lanes` field says which lanes it
has so far, so "62%, unit only" is never mistaken for a full figure.

### 1.5 Results and flaky tests

**Signals.** Each signal comes from the record:

| Signal                                                                                      | Detected from                                                                             | Strength  |
| ------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- | --------- |
| **Same-tree split.** The test passed in one run and failed in another on the same tree hash | the commit's runs (sharded jobs, re-runs, a local run plus CI)                            | confirmed |
| **Retry pass.** The test failed, then passed on retry, within one run                       | `go test -count=N` attempts in the stream, and `forge test flakes confirm` reruns (below) | confirmed |
| **History.** The test flips on the default branch                                           | outcomes over the last 30 days of default-branch runs                                     | ranked    |

**Per-test outcomes.** Every signal reads one record: per run, each test's
`(test id, lane, commit, run id, attempt, status)`. Nothing in the three
repos records this today, and phase 0 found it is the input the flake states
need. How it is stored without a row per passing test is in
[2.1](#21-store).

**Host failures first.** Before any verdict or score, each failure's output is
matched against host signatures: network and download errors,
`toomanyrequests`, ENOSPC and quota errors, `address already in use`, docker
CLI timeouts, and `panic: test timed out` on a loaded host. A match is a
`host` event. It never scores, and it is reported as host health, per runner
and per signature. In phase 0, at least 30 of the 77 `flake:suspected` issues
in the three repos had a host or harness cause, and about 40% of the tests
that would have reached `flaky` were in that group.

**Correlated failures.** Three or more failures in one run that share a
package, a harness (testsrv, ClickHouse, pgtest) or a failure signature are
grouped into one infra event, not counted as a flake event per test. Each of
those failures gets the verdict `correlated` and names the group, its
`infra_group`. The same co-failing set on a later commit is the same event,
with the same `infra_group`. This replaces
control-plane's `broad` cut-off (more than 15 failures in a run), which never
fired on real data: host failures came 2 to 8 tests at a time.

**Classification.** This uses control-plane's taxonomy, now
`pkg/testreport.Verdict`:

- `confirmed`: failed, then passed on the same tree.
- `real`: failed again on rerun.
- `raced`: the output reports a data race. A race is a bug and is never
  counted as a flake.
- `inconclusive`: the rerun could not run (a build failure, a timeout).
- `unattributed`: the failure could not be tied to a test.
- `host`: the failure matched a host signature (above). Not scored.
- `correlated`: the failure is part of a correlated group (above), named by
  its `infra_group`. The group is one infra event, and its tests are not
  scored for it.

**States.** A state counts confirmed events on **distinct commits**: several
confirmed events on one commit, a burst of re-runs for example, count once. A
commit where the test failed in **every** run is a real break, not a flake,
and does not count.

| State         | Enters when                                                 | Leaves when                                                           |
| ------------- | ----------------------------------------------------------- | --------------------------------------------------------------------- |
| `suspected`   | one confirmed event                                         | 14 days with no event → `healthy`                                     |
| `flaky`       | confirmed events on two or more distinct commits in 30 days | the test's file changes and then 14 days pass with no event → `fixed` |
| `quarantined` | a quarantine entry for it exists on the default branch      | the entry is removed (the test was deleted or rewritten)              |
| `removed`     | the test no longer appears in runs after it was flaky       | —                                                                     |

For forge, control-plane and reliant, `suspected` is the state people act on:
the owner's policy deletes a flaky test on its first event, so the issue and
the quarantine follow from `suspected`, not `flaky`. In phase 0 every test
later deleted as flaky would have reached `suspected` on its first confirmed
event, and the 17 tests with two or more events would have reached `flaky`
within two days of the second.

**Flake rate: a ranking, not a trigger.** `flake_score` is the Wilson lower
bound (95%) of `commits_with_confirmed_flake / commits_with_runs` over the
test's last 30 days, shown as the "flake rate" with its `n`. It orders the
flaky page and never moves a test between states. Its denominator `n` is the
completed default-branch runs of the test's lane in which the test ran, not
skipped, excluding cancelled runs (265 of about 1,000 forge runs in 30 days
were cancelled). Phase 0 showed a threshold would never decide anything at
our volumes: a lower bound of 0.02 needs 5 flaky commits at 100 runs, 17 at
500 and 29 at 1,000, while the worst known flake had 5 events in about 1,000
runs (0.002).

**Quarantine.** control-plane's format becomes forge's standard:
`ci/quarantine/<lang>/<package dir>/<Test>.yaml`, holding `{issue, reason,
signature, expires_at}`. There is one file per test, so concurrent quarantine
PRs never conflict.

- `forge test quarantine add <test-id> [--issue <url>] [--expires 48h]` writes
  the file.
- The scaffolded lanes pass `-skip "$(forge test quarantine skip-regex go)"`,
  the port of `scripts/lib/quarantine.sh`.
- An expired entry stays applied, and `forge test flakes` reports it as
  overdue.
- **For customers the product suggests a quarantine and never makes one.** The
  flaky page and `forge test flakes` show the command. `tests.flakes.quarantine:
pr` in forge.yaml makes the product open the quarantine PR itself, through the
  project's GitHub connection. Our three repos set it, matching the owner's
  "flaky = delete immediately" policy.

**Issues.** `tests.flakes.issues: true` makes the product file or update one
`flake:suspected` issue per suspected test, and a `flake:removed` issue on
quarantine. The issue carries the test body, the failure signature and the
runs, and the product closes `flake:suspected` when a test leaves that state.
This is the port of `flake-report.sh` and `flake-quarantine.sh`, done
server-side through the GitHub connection. It is off by default for customers
and on for our repos. `forge test flakes --issues` does the same locally with
`gh` for a project with no control plane.

**The flake gate becomes a gate step.** `forge test flakes gate --base <rev>`
is the port of `ci-changed-tests.sh` and reliant's `test-flake.sh`: it runs
every test the diff adds or edits `-count=3` (`-race` under `--full`). It joins
`forge ci` as a step, `flake-check`, through a lint rollout row. **For
customers it reports and never fails**: the row's grade stays `warn` until a
project opts in with `lint.rules: {flake-check: error}`, which our three repos
set. Its results are captured as a run, so its retry passes feed the record.

**Confirming red runs.** `forge test flakes confirm --run <ci-run-id> | --logs

<dir> | --from <results.json>` is the port of `flake-report.sh`. It reruns each
failed test once under `-race`, at most 15, classifies the results, and records
them as a run (`lane: confirm`). The flake state, issues and quarantine follow
from the record. It always exits 0 and never changes the verdict of the run it
read, which is the existing rule.

### 1.6 Where it runs

| Where                                          | What runs                                                                                                                                       | Upload                                                                                                                                                             |
| ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Locally** (`task test`, `forge test run`)    | capture into `.forge/tests/runs/`                                                                                                               | Off for a dirty tree. On a clean tree, only with `tests.upload: always` or `forge test report --upload`. A local clean run counts toward its commit like any other |
| **Scaffolded CI** (`ci.yml`)                   | the lanes through capture, then `forge test report --upload`                                                                                    | On, with a `tests:write` token in `FORGE_CONTROL_PLANE_TOKEN`, the variable forge reads everywhere else. Upload failure is a warning                               |
| **External CI**                                | `forge test report --upload` after the project's own steps, or `forge test ingest` for runners forge does not wrap (pytest JUnit, jest LCOV, …) | On with a token, like `forge release record`                                                                                                                       |
| **Preview envs** (scaffolded preview workflow) | deploy the `cover` variant, `task test:e2e ENV=<preview>`, then upload                                                                          | On                                                                                                                                                                 |
| **Dev**                                        | the dev stack runs `cover` builds, and `task test:e2e` collects from it                                                                         | Dirty-tree rules apply                                                                                                                                             |
| **Release cut** (`env build --release`)        | none: the release references its commit's report                                                                                                | —                                                                                                                                                                  |

**Release evidence.** `Release.Tests *TestsRef` is a new evidence field,
outside identity, backfilled and never replaced, like `SourceSBOM`. It holds
`{commit, report_digest, lanes, totals {passed, failed, skipped}, coverage
{combined, by_lane}, status ok|partial|unavailable, reason}`. On a
control-plane env the server fills it from the commit report when the release
is cut, and later as more lanes arrive. On the machine ledger forge fills it
from `.forge/tests/`. The scaffolded `release.yml` swaps `forge ci
verify-test-run --gate-json` for `forge test report --gate-json gates/test.json`.
That writes the same gate shape with the verify-test-run checks included, so
the deploy's `tests` gate keeps its meaning. `verify-test-run` itself keeps
working.

**Sign-in.** Like monitoring, the hosted record requires sign-in. A claimable
deploy has no test record until it is claimed.

### 1.7 CLI

The new `test` group is registered in `internal/cli/root.go` beside `release`
and `obs`:

```
reliant forge test run [<lane>...] [--json]                # runs the project's own `task <lane>` lanes as one run; never `go test` itself
reliant forge test begin                                   # plumbing: allocate (or reuse $FORGE_TEST_RUN) a run directory
reliant forge test capture --run <dir> --lane <name> -- <command...>   # plumbing: exec unchanged, record, pass the exit code through
reliant forge test collect <env> [--workload <w>] [--reset] [--run <dir>] [--lane e2e]
reliant forge test ingest --lane <name> --format junit|go-json|vitest|lcov|cobertura|istanbul <file>... [--run <dir>]
reliant forge test report [<run>... | --commit <sha>] [--base <rev>] [--upload[=required]]
                          [--gate-json <file>] [--format text|json|lcov|cobertura|html] [--json]
reliant forge test coverage [<path>] [--commit <sha> | --release <v> | --env <env>] [--lane <l>]
                            [--diff <base>] [--by package|file] [--json]
reliant forge test runs [--commit <sha> | --branch <b> | --release <v>] [--json]
reliant forge test flakes [--state suspected|flaky|quarantined] [--issues] [--json]
reliant forge test flakes gate --base <rev> [--full]       # changed tests ×3 (port of ci-changed-tests.sh)
reliant forge test flakes confirm --run <ci-run> | --logs <dir> | --from <file>   # port of flake-report.sh
reliant forge test quarantine add|remove|list|skip-regex [<test-id>] [--issue <url>] [--expires <dur>]
```

**About the verb name.** `forge test` was removed for running a suite of its
own. `forge test run` here only runs the project's Taskfile tasks: it refuses
with exit 4 when the Taskfile has no such lane, and its help says so in its
first line. Everything else in the group reads records. If the owner would
rather keep the old name unused, the group can live under `forge ci test …`
with the same subcommands. That is a rename, not a design change.

- Reads come from the control plane when the project records there, and from
  `.forge/tests/` otherwise. Text output ends with the console link
  (`https://forge.reliantlabs.io/forge/p/<project>/tests`).
- **Exit codes** use the shared table (`internal/cli/exitcodes.go`). Coverage
  never decides one:

| Code | `test run` / `capture`                                | `test report`                                                                                                                       | `flakes gate`                        |
| ---- | ----------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| 0    | the lanes' own exit codes were all 0                  | every captured test passed                                                                                                          | every changed test passed 3/3        |
| 1    | a lane failed (the command's own code is in the JSON) | at least one test failed in the runs reported                                                                                       | a changed test failed at least once  |
| 2    | —                                                     | undetermined: no results, zero evidence (verify-test-run's rule), or lanes from different trees                                     | no changed tests could be identified |
| 4    | refused: no such Taskfile lane                        | upload refused (not signed in, no `tests:write`) **only with `--upload=required`**. Plain `--upload` warns and keeps the run's code | —                                    |

- **`--json` documents** carry `obsout.Head` (`schema`, `ok`, `exit_code`,
  `error`) and `suggested[]`. Their JSON Schemas are published with `forge
project schema <name>`:

```jsonc
// forge.test.report/v1
{
  "schema": "forge.test.report/v1",
  "ok": true,
  "exit_code": 0,
  "project": "shop",
  "commit": "9f3c…",
  "tree": "a1b2…",
  "dirty": false,
  "base": { "sha": "77e0…", "report": true },
  "runs": [
    {
      "id": "01JD…",
      "lane": "unit",
      "source": "ci",
      "url": "…",
      "exit_code": 0,
    },
  ],
  "totals": {
    "passed": 1412,
    "failed": 0,
    "skipped": 37,
    "flaky_retry_pass": 1,
  },
  "coverage": {
    "granularity": "statement",
    "lanes": { "unit": 61.4, "integration": 38.0, "e2e": 52.7 },
    "combined": 74.2,
    "only_e2e": 3.1,
    "delta": 0.6,
    "diff": { "lines": 212, "covered": 171, "pct": 80.7 },
    "excluded": { "generated_statements": 9120 },
  },
  "packages": [
    {
      "path": "internal/billing",
      "lang": "go",
      "statements": 840,
      "combined": 58.3,
      "lanes": { "unit": 41.0, "e2e": 50.2 },
    },
  ],
  "failures": [
    {
      "test": "go:…/internal/billing:TestRefund",
      "file": "internal/billing/refund_test.go",
      "line": 88,
      "output": "…",
    },
  ],
  "flakes": [{ "test": "…", "verdict": "confirmed", "signal": "retry_pass" }],
  "upload": {
    "status": "ok",
    "url": "https://forge.reliantlabs.io/forge/p/shop/tests/runs/01JD…",
  },
  "suggested": [
    "reliant forge test coverage internal/billing --diff origin/main",
  ],
}
```

The other documents:

- `forge.test.coverage/v1` is the stored per-file document (blocks and hits
  per lane, plus the changed line ranges).
- `forge.test.coverage-view/v1` is what `forge test coverage --json` prints:
  `summary`, `rollup` (by package or file), `diff` and `suggested`, with the
  stored `forge.test.coverage/v1` document embedded as `coverage`.
- `forge.test.flakes/v1` is `{tests: [{id, state, score, n, events: [{kind,
commit, run, at}], quarantine, issue}], host: [{signature, events, runs}]}`,
  where `score` with `n` is the flake rate (a ranking) and `host` is host
  health.
- `forge.test.runs/v1` is `forge test runs --json`.
- `forge.test.run-result/v1` is `forge test run --json`: the lanes it ran and
  each one's exit code.
- `forge.test.collect/v1` is `forge test collect --json`.
- `forge.test.confirm/v1` is `forge test flakes confirm --json`.
- `forge.test.run/v1` is the `run.json` manifest.

### 1.8 forge.yaml

```yaml
tests:
  coverage: true # default. false: lanes capture results only
  # coverage.packages: package (default) | module
  # coverage.exclude: ["internal/legacy/**"]
  # coverage.include_generated: false
  upload: ci # ci (default) | always | never
  flakes:
    quarantine: suggest # suggest (default) | pr
    issues: false # file flake:suspected / flake:removed issues
```

The existing `ci.test.coverage` key keeps working: `true` means `tests.coverage:
true`, and the old upload-as-artifact step stays in projects that have it.

## 2. control-plane

### 2.1 Store

The migrations are expand-only. Every table carries `org_id` (CASCADE with the
org) and is exempt from appfence (evidence, not user intent):

```sql
-- One per uploaded lane run. Idempotent on the report digest.
test_runs(id, org_id, project, git_commit, tree_hash, dirty, branch, base_sha,
          lane, source ci|local|env|confirm, environment_id NULL, ci_provider, ci_run_url,
          started_at, finished_at, exit_code, passed, failed, skipped, retry_passed,
          results_digest, coverage_digest NULL, forge_version, uploaded_by, created_at,
          UNIQUE (org_id, project, results_digest))

-- Dictionary of tests, so case rows stay narrow.
test_keys(id, org_id, project, lang, package, name, file, line, first_seen_at,
          UNIQUE (org_id, project, lang, package, name))

-- Which tests a run ran, per package: a digest of the sorted test-key list.
-- Test sets rarely change between commits, so each distinct set is stored once.
test_run_packages(run_id, org_id, package, test_set_digest, attempts_per_test,
          PRIMARY KEY (run_id, package))
test_sets(org_id, digest, test_key_ids bigint[], PRIMARY KEY (org_id, digest))

-- Row condition: a test has rows iff at least one of its attempts in the run
-- did not pass (fail or skip); it then has one row for EVERY attempt, from 1,
-- passing ones included, so a retry pass is stored as fail, pass. A test whose
-- attempts all passed has none. At most 200k rows per run (below).
test_case_results(run_id, org_id, test_key_id, attempt, status pass|fail|skip,
          duration_ms, failure_sig_hash, verdict NULL,
          PRIMARY KEY (run_id, test_key_id, attempt))

-- Raw and normalised documents in object storage; vulnstore's copy-state machine verbatim.
test_blobs(org_id, digest, kind results|coverage|diff|raw, size_bytes, storage_key,
          copy_state present|restoring|deleting|absent, copy_claim, copy_claimed_at,
          PRIMARY KEY (org_id, digest))

-- The accumulated report per clean commit (recomputed as runs arrive).
test_commit_reports(org_id, project, git_commit, tree_hash, lanes text[], passed, failed,
          skipped, coverage_combined, coverage_by_lane jsonb, diff_lines, diff_covered,
          merged_coverage_digest, updated_at, PRIMARY KEY (org_id, project, git_commit))
test_commit_packages(org_id, project, git_commit, package, lang, statements,
          covered_combined, covered_by_lane jsonb, PRIMARY KEY (org_id, project, git_commit, package))

-- A release's report: a join table, so deploy_release_artifacts stays append-only and untouched.
deploy_release_tests(release_id, org_id, git_commit, PRIMARY KEY (release_id))

-- Flake state per test, and the events that drove it.
test_flakes(org_id, project, test_key_id, state, score, score_n, confirmed_commits_30d,
          first_seen_at, last_event_at, quarantined_at, issue_url, updated_at,
          PRIMARY KEY (org_id, project, test_key_id))
test_flake_events(id, org_id, project, test_key_id, kind same_tree|retry_pass|confirm,
          verdict, git_commit, run_ids bigint[], failure_sig_hash, at)

-- Host and correlated failures: never scored, shown as host health.
-- kind infra: one row per correlated group; its failures carry this infra_group.
test_infra_events(id, org_id, project, kind host|infra, infra_group, test_key_ids bigint[],
          git_commit, run_ids bigint[], failure_sig_hash, runner, at)
```

- A **project** here is the forge project name, as in releases. A project that
  never cut a release can still record tests: forge itself has no deployed env.
  So the first `RecordTestRun` registers the project identity under the same
  rule `CutRelease` uses (C2).
- **No source code is stored.** Coverage keeps file paths, positions, counts
  and each covered file's git blob id. The browser fetches source from the
  linked GitHub repo at view time
  ([Amendment 1, part A](#a-where-the-coverage-browser-gets-source)).
- **Every per-test outcome is kept; pass rows are implied, not stored**
  ([Amendment 1, part B](#b-retention)). The flake states need the
  `(test id, lane, commit, run id, attempt, status)` tuple for every test in
  every run ([1.5](#15-results-and-flaky-tests)), and it is recoverable
  exactly:

  - `test_runs` gives the lane and the commit.
  - `test_run_packages` names each package the run ran, the set of tests it
    ran there, and how many attempts each got (`-count`).
  - `test_case_results` stores every attempt, pass included, of each test
    that had **any** non-pass attempt in the run. So a retry pass is stored
    as `fail, pass`, which is what retry-pass classification reads.
  - A test whose attempts all passed has no row. It is in the run's test set,
    and its pass is implied by it.

  The test set is exact where a package list is not: a lane run with `-run`
  (the flake gate), a build-tagged test, or a test added or deleted on that
  commit. For our repos this removes about 99% of the rows.

  | A test's attempts in one run                | `test_case_results` rows     | Read back as            |
  | ------------------------------------------- | ---------------------------- | ----------------------- |
  | passed (`-count=1`, or 3/3 with `-count=3`) | none                         | pass, from the test set |
  | failed                                      | `1 fail`                     | fail                    |
  | failed, then passed on retry                | `1 fail`, `2 pass`           | pass, retry pass        |
  | failed twice, then passed (`-count=3`)      | `1 fail`, `2 fail`, `3 pass` | pass, retry pass        |
  | passed, then failed (`-count=2`)            | `1 pass`, `2 fail`           | fail (the last attempt) |
  | skipped (`t.Skip`)                          | `1 skip`                     | skip                    |

  The last attempt is the test's outcome in the run. A pass after a fail is
  a retry pass.

- **Caps, never truncation.**

  - `RecordTestRun` refuses an upload over 32 MiB across its parts as sent
    (`gzip` required for a part above 1 MiB), or over 256 MiB inflated across
    its parts, with `InvalidArgument` and the reason. It parses nothing, so
    it cannot count cases.
  - **The case cap is 200k stored attempt rows per run, counted at ingest.**
    It counts `test_case_results` rows, not every attempt a run made: a lane
    of 70k tests run `-count=3` that all pass stores no rows. Ingest refuses
    a run over the cap: the run is settled failed with the reason, and none
    of its results are written. It is never truncated.

- **Sizes.** Phase 0 measured the three repos: a full
  unit lane's per-package profile is about 9, 13.5 and 17 MB as text and 0.9,
  1.2 and 1.5 MB gzipped (forge, control-plane, reliant), and one server's
  covdata is a 1.15 MB gzipped meta file plus 5 to 18 KB of counters per
  process exit. A commit's raw upload (unit, integration and one server) is
  2 to 4 MB gzipped, and about 1 MB gzipped once merged into per-file
  blocks.

### 2.2 Ingest, merge and flake detection

- **`RecordTestRun`** stores the bytes and returns `accepted`. It does not
  parse in the request. It re-derives each digest, binds the blobs, and inserts
  the `test_runs` row. Uploads are untrusted input and can be large.
- **`test_ingest`**, a forge worker on `workerloop` with a singleton lease,
  claims unprocessed runs and does the following:
  1. It parses them with `pkg/testreport`, in a subprocess with CPU, memory
     and time limits (the `vuln_scan` rule).
  2. It writes the run's test sets and case rows.
  3. It recomputes the commit report: the merge of all of the commit's
     coverage documents, written to a new merged blob with
     `test_commit_reports` and `test_commit_packages` updated in one
     transaction. Coverage from a different build of a package (another
     `covmeta` hash, or a statement-count mismatch on a block key) is not
     merged, and the run records why ([1.4](#14-merging-and-accumulating-across-lanes)).
  4. It matches each failure against the host signatures and groups
     correlated failures, writing `test_infra_events`. This runs **before**
     scoring, so a host or infra failure never becomes a flake event.
  5. From the failures that remain, it writes flake events for same-tree
     splits and retry passes, and recomputes the affected tests' states and
     flake rates.
  6. It binds the commit report to any release cut from that commit.
- **`test_flake_sweep`**, daily, ages states out (`suspected → healthy`,
  `flaky → fixed`). It reconciles `quarantined` against what the default branch
  reports, and on opt-in projects it files and closes issues and opens
  quarantine PRs through the GitHub connection (once per test and transition).
- **`test_retention`**, daily, collects blobs and runs past the retention
  rules (a run's test-set references and case rows go with it, by cascade;
  the case table is not partitioned) ([Amendment 1, part B](#b-retention)), keeping anything a retained release references
  (`deploystore.RetainedReleaseIDs`, the vulnstore rule).
- **Fairness.** Per-org concurrency caps and bounded batches, as in
  `vuln_rescan`.

### 2.3 API

The new service lives in package `controlplane.v1`, at
`proto/services/tests/v1/tests.proto`. It is named `TestsService`, not
`TestService`, because forge requires a service's name to match its proto
directory (`tests/`); the owner settled the name on 2026-11-21
([Decisions](#decision-2026-11-21-service-names)). The web reaches every `controlplane.v1.*` service
directly on platform-api, so it needs no proxy:

```proto
service TestsService {
  // Writes (tests:write, or deploy:write).
  rpc RecordTestRun(RecordTestRunRequest) returns (RecordTestRunResponse);
  //   project, commit, tree, dirty, branch, base, lane, source, env, ci{provider, url},
  //   results (bytes, encoding), coverage? (bytes, encoding), diff? (changed line ranges); at least one part

  // Reads (tests:read). Every filter is optional; lists are paged.
  rpc GetTestSummary(GetTestSummaryRequest) returns (GetTestSummaryResponse);
  //   per branch (default branch by default): coverage trend points (commit, at, combined, by_lane),
  //   pass rate over the last N runs, flaky / quarantined counts, last run, release markers,
  //   host health: one row per host signature {signature, events, runs, tests}
  rpc ListTestRuns(ListTestRunsRequest) returns (ListTestRunsResponse);
  //   filters: branch, commit, release, environment, lane, source, status
  rpc GetTestRun(GetTestRunRequest) returns (GetTestRunResponse);
  //   totals, failures with output (each TestFailure carries its verdict and infra_group),
  //   retry passes, slowest tests, zero-evidence packages
  rpc GetCoverage(GetCoverageRequest) returns (GetCoverageResponse);
  //   project + one of commit | release | environment; lane?; path prefix; base?
  //   → the children of that path (package or file) with statements, combined and per-lane %, diff %
  rpc GetFileCoverage(GetFileCoverageRequest) returns (GetFileCoverageResponse);
  //   per line: hits per lane, partial flag, changed flag; never source text
  rpc GetReleaseTests(GetReleaseTestsRequest) returns (GetReleaseTestsResponse);
  //   the release's report, plus the delta against the previous release in the same project
  rpc ListFlakyTests(ListFlakyTestsRequest) returns (ListFlakyTestsResponse);
  //   filters: state, min_score, package
  rpc GetTest(GetTestRequest) returns (GetTestResponse);
  //   one test: outcome history (last 50 runs), flake events, failure signatures grouped, quarantine, issue
}
```

A flake event's verdict is the `TestFlakeVerdict` enum, the proto twin of
`pkg/testreport.Verdict` ([1.5](#15-results-and-flaky-tests)). Only
`CONFIRMED` scores. `HOST` and `CORRELATED` never do; a `CORRELATED` event
carries `infra_group`, the id of the correlated-failure group it belongs to,
which is the `infra_group` of its `test_infra_events` row
([2.1](#21-store)).

Two read shapes follow from that, and cp#3286's proto does not have them yet,
so a later proto change adds both:

- **`TestFailure` carries `infra_group`** in `GetTestRun`, beside its
  verdict, so a run page can group its correlated failures into one infra
  event instead of listing them as separate failures.
- **`GetTestSummary` returns host-health rows**, one per host signature:
  `{signature, events, runs, tests}`, the shape of `pkg/testreport`'s
  `HostHealth`. That replaces a bare count (`host_failures_30d`) as what the
  Flaky tests page's Host health list reads.

`TestsAdminService` (`proto/services/tests_admin/v1/`) gets
`RecomputeCommit`, `RecomputeFlakes`, `PurgeNow` and `GetIngestBacklog` for
`control-plane ops call`.

**Capabilities.** A `test_features` list goes on `DeployService/GetCapabilities`,
with the withheld set used until each name's behaviour is live:
`test-results`, `test-coverage`, `flake-scores`, `release-test-evidence` and
`refuses-cover-variant`. On a control plane without the service, forge sees
`Unimplemented`, prints one note ("this control plane does not store test runs
yet") and keeps the local record.

### 2.4 Tenancy and grants (fails closed)

- Every handler resolves the org with the `callerOrgID` family
  (`internal/handlers/deploy/helpers.go:247-433`) and scopes every query by it.
  Another org's project is `NotFound`. Env-bound tokens see only runs recorded
  against their env (`callerOrgIDForEnvRead`).
- **New scopes** in forge's `pkg/accesstoken`, re-exported by control-plane.
  The `access_tokens` CHECK is widened with `forge db migration new
--closed-set` ([Decision 4](#9-decisions-for-the-owner)):
  - **`tests:write`**, a narrow upload scope like `sourcemaps:write`. A PR's CI
    job needs it and should not hold `deploy:write`. `RecordTestRun` also
    accepts `deploy:write`, so a deploying CI needs no new token.
    `tests:write` implies nothing, not even `tests:read`, so a PR's CI token
    can upload results but cannot read failure output back. It is not
    env-bindable.
  - **`tests:read`**, granted on day one to every holder of `deploy:read` and
    added to the Admin preset. Failure output can quote config, request bodies
    and log lines, so it is not readable by every token that can record a
    deploy. It is env-bindable like `vuln:read`: a token bound to an env reads
    only the runs recorded against that env.
  - **The grant goes to roles and presets, not to existing tokens.** Machine
    tokens that already hold `deploy:read` are not rewritten to add
    `tests:read`. A token minted before the release that ships Tests must be
    re-minted to read Tests. This fails closed: an old token reads nothing it
    could not read before. It is also reversible, because removing the scope
    from a role or preset rewrites no stored token either.
- An operation with no scope mapping is refused for machine principals, and
  grants are re-read on every request. These are the existing rules.
- Quarantine and issue settings are never written through the API. They are
  forge.yaml, changed in a PR.

### 2.5 Retention and billing

> **Superseded** by [Amendment 1, part B](#b-retention) and decision 5. One
> rule set, the same for every org (default branch's newest 50 reports plus
> daily summaries, other branches latest-only, releases keep theirs), counted
> against the org's global storage tier and tracked, not charged, until it
> exists. The text below is the original proposal, kept for the record.

- **Retention is a plan-catalog block,** the template being
  `product_events_retention_days`: `tests.retention_days {free: 30, paid: 180}`.
  It applies to raw blobs, per-file coverage and case rows.
- **Kept longer:** commit summaries and per-package rows for the default
  branch's daily head (400 days), and everything a retained release references,
  for as long as that release is retained.
- **No new meter at launch.** The caps bound storage, and phase 0's numbers
  tell whether a `test_storage` meter is ever worth adding
  ([Decision 5](#9-decisions-for-the-owner)).

## 3. Reliant web

### 3.1 Where it lives

The nav is data (`navConfig/hybrid.ts`, `items.tsx`), and every project gets
the same nav. The order on `feat/web-project-shell-proto` is: Overview · Inbox
· Agents › · Dashboards ▾ · Observability › · Analytics ▾ · Deployments ▾ ·
Data · Storage · Auth · Security ▾ · Configure ▾.

| Option                                                                                   | Shape                              | For                                                                                                                                           | Against                                                                                                           |
| ---------------------------------------------------------------------------------------- | ---------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| **(a) A Tests group** after Deployments: Tests ▾ = Overview, Coverage, Runs, Flaky tests | a first-class group, like Security | Tests are per commit and branch, and most runs never become a release. Coverage browsing and flake triage are daily work, so they need a home | one more top-level group                                                                                          |
| (b) A tab inside a release only                                                          | `?tab=tests` on release detail     | no nav change                                                                                                                                 | Hides everything that is not a release: PR runs, main's trend, flakes. Forge itself has no releases to hang it on |
| (c) A "Quality & security" group: Tests pages beside Vulnerabilities and SBOMs           | rename Security, add rows          | one place for "is this safe to ship"                                                                                                          | Mixes two audiences and cadences. Six or more rows in one group. Undoes a settled Security label                  |

**Recommendation: (a)**, plus the release tab and the Overview card as views
onto the same data. The group is visible in every project and never hides on an
empty read. An empty page shows "No test runs yet" with the one command for the
project's situation. The Overview row carries a badge, like the alerts badge.
By default it counts `flaky` tests, not `suspected`. A project whose flake
policy acts at `suspected` (`tests.flakes.issues: true` or `quarantine: pr`,
which includes forge, control-plane and reliant) counts `suspected` plus
`flaky` instead ([Decision (2026-11-21): nav badge count](#decision-2026-11-21-nav-badge-count)).
Proposed order: … Deployments ▾
· **Tests ▾** · Data · Storage · Auth · Security ▾ · Configure ▾.

Paths (`TESTS_SEGMENT = "tests"`): `/forge/p/<project>/tests`,
`/tests/coverage?path=<pkg/or/file>&commit=&lane=&base=`,
`/tests/runs[/<id>]` and `/tests/flaky[/<test-id>]`. File paths go in the
search param, because `consolePageOf` splits only fixed segments. The org form
`/o/<org>/apps/<project>/tests…` comes with the route tree. An org-wide
rollup is a follow-up (`OrgPage`).

### 3.2 The pages

**Overview** (`/tests`):

```
Tests · shop                                   branch [main ▾]   env [all ▾]
┌──────────────┬──────────────┬──────────────┬──────────────┐
│ Coverage     │ Pass rate    │ Flaky tests  │ Last run     │
│ 74.2%  ▲0.6  │ 98.7%        │ 4 (2 quar.)  │ 12 min ago ✓ │
│ unit 61 · int 38 · e2e 53   │ last 50 runs │ → triage     │ CI #4411     │
└──────────────┴──────────────┴──────────────┴──────────────┘
Coverage over time  ── combined ── unit ── integration ── e2e      v1.8  v1.9 (release markers)
  80 ┤                                         ╭──────
  70 ┤──────────────╮      ╭──────────────────╯
  60 ┤              ╰──────╯
Lowest coverage among recently changed packages      Recent runs            Flaky tests
internal/billing   58% ▼8   [Raise coverage]          main 9f3c ✓ 1412/0     TestRefund   0.11 flaky
internal/webhooks  61%      [Raise coverage]          pr-88 1a2b ✗ 2 fail    TestSyncJob  0.04 quar.
```

**Coverage** (`/tests/coverage`): a tree table, package → file, with columns
for statements, combined %, one bar per lane, and diff % when a base is set.
The toggles are lane, "only e2e", and "changed files only". A file opens the
annotated source:

```
internal/billing/refund.go  @ 9f3c  · 71% combined · unit 55 · e2e 66 · diff 4/6 lines
  41 │ 12 3 │ func Refund(ctx context.Context, o Order) error {
  42 │ 12 3 │     if o.Paid == 0 {
  43 │  0 0 │ ▌       return ErrNothingToRefund          ← uncovered (red)
  44 │ 12 3 │     }
  45 │  4 0 │ ◐   if o.Partial && o.Remaining() < o.Paid {   ← partial (amber); changed line marked ▸
     └ hits: unit e2e
```

The source comes from GitHub at that commit through the project's linked repo.
Without a linked repo, the view shows line numbers and hits with a "link a
repository to see source" note. `LightweightCodeViewer` gains a per-line tone
and gutter prop, which is lighter than Monaco decorations.

**Runs** (`/tests/runs`): a table with when, commit, branch, source (CI /
local / preview env), lanes, passed / failed / skipped, duration and coverage.
The filters are branch, lane, source and status. A run's page shows failures
with output and file:line, retry passes, the slowest tests (the
`test-budget` view), and zero-evidence packages (the `verify-test-run` rule).

**Flaky tests** (`/tests/flaky`): a table with test, package, state, flake
rate with its n (the sort order), signals (same-tree / retry / history), last
seen and issue. Below it, **Host health** lists host and infra events by
signature and runner, which are never counted against a test. A test opens a
drawer:

- a strip of outcomes over the last 50 runs;
- failure signatures, grouped;
- the commits where it split;
- its quarantine entry and issue.

Actions on a test or a selection: **Fix with agent**, **Quarantine** (opens the
PR on opt-in projects, otherwise shows the command), and **Open issue**.

**Elsewhere:**

- Release detail gets a **Tests** tab (`ReleaseTab`, `TestsTab` in
  `ReleaseTabs.tsx`): the release commit's totals and per-lane coverage, the
  delta against the previous release, and new failures and flakes since then.
- `ReleasesList` gets a **Tests** column beside Checks: a pass mark plus
  combined %.
- The Overview landing gets a **Tests** card beside Releases: coverage with a
  sparkline, pass rate and flaky count. "Needs attention" gains "N new flaky
  tests" and "coverage of `<pkg>` fell N points in `<release>`". Where the
  project's flake policy acts at `suspected`, it also gains "N suspected
  tests", and the nav badge counts them too (3.1). The summary returns the
  policy, so the web never guesses it.

### 3.3 Data path

This copies the Vulnerabilities stack:

- `services/controlPlane/tests/{testApi,wire,types,errors,TestApiContext}.ts`
  over `getControlPlaneClient(TestsService)`, with `isTestsServiceMissing`
  driving an "isn't available here yet" state.
- `hooks/test-queries.ts`: `useTestSummary`, `useTestRuns` (infinite),
  `useTestRun`, `useCoverage`, `useFileCoverage`, `useReleaseTests`,
  `useFlakyTests` and `useTest`, keyed `[tests, orgId, project, …]`.
- Fixtures registered for the preview harness.
- The TypeScript stubs come from the vendored control-plane proto (`task
generate:controlplane`).

## 4. Agent tie-in

Both actions start the same way Security's "Fix with agent" does today:
AskReliant, a branched worktree plus a chat with a structured prompt. They then
graduate to built-in workflows that the web starts with `RunService.StartRun`,
linking the run and then the PR.

**Fix this flaky test.**

- **AskReliant subject** `{kind: "flaky-test", test, package, file, line,
score, signatures, splitting_commits, quarantine}`. The prompt carries the
  owner's policy: make the test deterministic or delete it. Never add sleeps,
  retries or a looser assertion. A race is a bug to fix, not a flake.
- **Workflow `fix-flaky-test.yaml`**, shaped like `autopilot-issue-worker`:

  1. `create_worktree`.
  2. The agent works with the testing skills loaded.
  3. Verify: `go test -run '^<Test>$' -count=50 -race <pkg>` must pass 50/50,
     plus `forge test flakes gate --base origin/<default>`.
  4. Remove the quarantine entry in the same change.
  5. `git_publish` → `github/pr.create@1`, linking the issue.

  A red verify is a report, and no PR is opened. `max_agent_iterations`
  bounds the cost.

**Raise coverage of `<pkg>`.**

- **AskReliant subject** `{kind: "coverage", package, current, target,
uncovered: [file:line ranges], lane}`.
- **Workflow `raise-coverage.yaml`.** The agent writes tests with forge's
  testing skills (`testkit`, `tdd`, the scaffolded per-rpc tests). It is
  verified with `task test -- ./<pkg>/...`, then `forge test coverage <pkg>
--json` must reach the target, then the flake gate passes on the new tests
  (3/3). The PR body shows coverage before and after.
- **Guard against padding.** A test with no assertion, or one that only calls
  code, does not count. The workflow's review step checks each new test for an
  assertion on behaviour, and the PR lists them.

**Automation (opt-in, off by default):** a `source: tests` trigger, the same
shape as `source: alerting`, can run `fix-flaky-test` when a test enters
`flaky`, or `raise-coverage` weekly on the lowest changed packages. Agent runs
cost LLM spend, so nothing starts on its own unless the project adds the
trigger.

## 5. Default-on, existing projects, and never blocking

### 5.1 New scaffolds

`forge project new` (every kind with Go or a frontend) writes:

- **Taskfile:** `test`, `test:integration`, `test:e2e` and `test:frontend`
  through `forge test begin` / `capture`, with coverage flags and the
  quarantine `-skip`. `test:short` is unchanged. `task coverage` stays, as
  `forge test report --format html`.
- **Frontends:** the `vitest.config.ts` coverage block, and
  `@vitest/coverage-v8` pinned.
- **Go builds:** the Dockerfile `cover` stage, `air.toml` reading
  `$FORGE_GO_BUILD_FLAGS`, and serverkit's `/debug/coverage` (a library change,
  so existing projects get it on their next forge pin).
- **forge.yaml:** a `tests:` block with the defaults shown, so the knobs are
  discoverable.
- **KCL:** nothing. The computed default instruments dev and previews.
- **CI templates:**
  - `ci.yml` uploads with `forge test report --upload`;
  - the preview workflow runs e2e and collects;
  - `release.yml` uses `forge test report --gate-json`;
  - `forge test flakes gate` runs as a reporting step.
- **Skills:** a `testing/coverage` and a `testing/flakes` skill, plus
  `benefits.md` entries.

A site with no tests gets the frontend block only. A project with no lanes
gets no capture.

### 5.2 Existing projects: the `tests` playbook

Taskfiles, Dockerfiles, `air.toml`, `vitest.config.ts` and CI workflows belong
to the project once scaffolded, so forge never rewrites them on `generate`. A
migration playbook, `migrations/v1.N/tests`, is surfaced by `forge project
upgrade list`:

- **Automation step (codemod).** It recognises each scaffolded lane by its
  scaffolded shape (with the project's flag edits tolerated) and rewrites it
  through capture. It adds the Dockerfile `cover` stage after `debug`, the
  `air.toml` flags, the Vitest block and the CI steps. A lane it does not
  recognise is listed, not touched.
- **Agent steps** cover the rest: hand-written lanes, custom CI, a monorepo
  with several Taskfiles.
- **Without the upgrade**, nothing changes for the project. `forge test ingest`
  and `forge test report --from <go test -json file>` still work over whatever
  it already produces. The scaffolded `release.yml` already writes `test.json`,
  so even an un-upgraded project gets results on the release.
- **A warn-first lint rule, `tests-capture`,** reports a Taskfile `test*` lane
  that runs `go test` without capture, as a warning that names the playbook. It
  gets a rollout row like every rule, and its grade never goes past `warn`
  unless the project opts in.

### 5.3 Never blocking: the guarantees

- `capture` and `run` exit with the lanes' own codes. forge adds no failure
  and hides none.
- Upload failure is a warning (`--upload=required` is the opt-in to fail).
- Coverage, its delta and diff coverage never decide an exit code or a
  promotion.
- The default `coverage` is off for persistent envs, and a `cover` variant can
  never be promoted to one. forge and the control plane both refuse.
- `flake-check` and `tests-capture` grade as `warn` unless opted in. Quarantine
  PRs, issues and agent automation are opt-in.
- **An opt-in test gate** comes later ([phase 6](#phase-6-follow-ups-each-optional)):
  `delivery_policy.test_gate = forge.TestPolicy {mode = "audit"}`. Its rules
  are no failed tests in the release commit's report, a minimum diff coverage,
  and coverage that does not decrease. It defaults to `audit`, with a rollout
  row, like `vuln_gate`.
- `tests.coverage: false` and `FORGE_TESTS=off` are the escape hatches.

## 6. Dogfood: forge, control-plane, reliant

Each repo moves in two steps. First the local part, as soon as phase 1 has
shipped and been pinned: capture in its lanes, and its flake scripts replaced
by `forge test flakes`. Then the hosted part, after the control-plane service
is live: upload, the Tests pages showing the repo, and the flake issue flow
done by the product.

| Repo                   | Local (after phase 1)                                                                                                                                                                                                                                                                                                                                                                                                        | Hosted (after phase 2)                                                                                                                                                                                                            | What it retires                                                                    |
| ---------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| **forge** (D1)         | The three `task test` lanes, `test:e2e` and the CI shards through capture. `ci-changed-tests.sh` → `forge test flakes gate`. `flake-report.yml` calls `forge test flakes confirm`. `lint.rules: {flake-check: error}`. **E2e coverage of the CLI itself:** `e2e-prepare` builds the forge binary with `-cover`, and the e2e shards set `GOCOVERDIR`. That is integration coverage for the CLI, which unit tests cannot reach | CI and main upload. The project "forge" records tests with no deployed env. `tests.flakes: {issues: true, quarantine: pr}`                                                                                                        | `scripts/ci-changed-tests.sh`, `scripts/flake-report.sh`, its workflow logic       |
| **control-plane** (D2) | `task ci` / `ci:full` lanes through capture. `flake-report.sh` and `flake-quarantine.sh` → `forge test flakes confirm` / `quarantine`. `scripts/lib/quarantine.sh` → `forge test quarantine skip-regex` (same file format, no data migration). The dev and e2e k3d envs run `cover` builds of admin-server and the workers, and `task test:e2e` collects                                                                     | `ci-full-main.sh` uploads every run. **`refs/notes/ci-full` stays the release gate's record until the product's `release-test-evidence` has matched it for two weeks**, then control-plane's release-cut script reads the product | the flake scripts; later, the git-notes store                                      |
| **reliant** (D3)       | Merge-gate and release-gate lanes through capture. `test:flake` (`test-flake.sh`) → `forge test flakes gate`. `flake-report.sh` → `confirm`. The dev stack's air builds instrumented. Vitest coverage for `frontends/web` (lane `frontend`)                                                                                                                                                                                  | CI and main upload. The Electron and visual e2e lanes come with phase 6's Playwright work                                                                                                                                         | `scripts/test-flake.sh`, `scripts/flake-report.sh`, `scripts/test-flake-report.sh` |
| landing-page           | No tests today. The Vitest block applies once it has any                                                                                                                                                                                                                                                                                                                                                                     | —                                                                                                                                                                                                                                 | —                                                                                  |

The repos' owner policies (flaky means delete, quarantine within 48 hours,
race means bug) become project settings: `quarantine: pr`, `issues: true`,
`flake-check: error`, and the `expires_at` default. They are not three copies
of a script.

## 7. Semver and backwards compatibility

Everything is an addition:

- **forge:**
  - New verbs: the `test` group.
  - New packages: `pkg/testreport` (public, v1).
  - New fields, each `omitempty` and outside identity: `Release.Tests`, the
    `cover` variant.
  - New KCL: `forge.Tests`, `Bundle.tests`, `GoBuild.coverage`,
    `DockerBuild.coverage`.
  - New forge.yaml: `tests.*`.
  - New gate step `flake-check` and lint rule `tests-capture`, each behind a
    rollout row that stays `warn`.
  - serverkit's `/debug/coverage` lives only on the localhost debug listener
    and answers 404 for a non-cover binary.
  - `forge ci verify-test-run`, `test-budget`, `task coverage` and
    `ci.test.coverage` keep their meaning.
  - Templates are the project's own, so existing projects change only through
    the playbook.
  - Phase 1 adds a test that an older forge's ledger decoder ignores
    `Release.Tests`.
- **control-plane:**
  - A new proto file and service.
  - A new `test_features` field on `GetCapabilities`.
  - New tables, and the widened scope CHECK (expand).
  - `Promote`'s refusal of a `cover` digest on a persistent env applies only to
    releases that carry a `cover` variant, which only a new forge produces.
  - `buf breaking` passes.
- **reliant:**
  - New routes and components, two new built-in workflow files, and new
    AskReliant subjects.
  - It uses the existing `StartRun`. No reliant proto change.

## 8. Phased plan

Sizes: **S** is under a day, **M** a few days, **L** a week or more. Tasks in
the same parallel group can run at the same time. → means "blocked by".

### Phase 0: measure (2 tasks)

Done on 2026-11-20/21. The results and the amendments they led to are in
[Amendment 2](#amendment-2-phase-0-results-2026-11-21). The `/debug/coverage`
prototype, SIGTERM collection in k3d and the Vitest LCOV merge were not
covered, and move into F5 and F1.

| #   | Task                                                                                                                                                                                                                                                                                                                                                                                                                          | Repo  | Size | Depends on |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- | ---- | ---------- |
| 0.1 | Spike on forge, control-plane and reliant, writing the numbers into this doc: `task test` with and without `-cover` (build time, test time, profile size, cache hit rate with `-coverprofile` under `-race` across many packages); `-coverpkg=<module>/...` cost; covdata from a `-cover` serverkit binary through a `/debug/coverage` prototype and through SIGTERM in k3d; `covdata merge` across two builds of one package | forge | S    | —          |
| 0.2 | Spike: one merge across a unit profile, a `covdata textfmt` profile and a Vitest LCOV; check the result against `go tool cover -func`; generated-file exclusion; the Wilson score on control-plane's last 60 days of `flake:suspected` issues (does 0.02 separate the known flakes?)                                                                                                                                          | forge | S    | —          |

### Phase 1: forge, local (no server)

| #   | Task                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Repo  | Size | Depends on       | Parallel group |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- | ---- | ---------------- | -------------- |
| F1  | `pkg/testreport`: results model and parsers (go-json, JUnit, Vitest), coverage model (Go profile, covdata via `textfmt`, LCOV, Cobertura, istanbul), merge (block-key dedupe, `covmeta`-hash grouping, boolean `set` merge), views, diff coverage, verdict taxonomy with `host` signatures and correlated grouping, Wilson flake rate; the `forge.test.*/v1` schemas. `internal/testreport` (verify-test-run) moves onto its parser, behaviour unchanged | forge | M    | 0.2              | A              |
| F2  | `forge test begin/capture`: run dir and manifest, human rendering of `-json`, exit-code passthrough, `FORGE_TESTS=off`, secret scrub and truncation                                                                                                                                                                                                                                                                                                      | forge | S    | F1 (parser only) | A              |
| F3  | `forge test report/coverage/runs` on the local store: views, `--format lcov/cobertura/html`, `--gate-json`, `--base` diff, exit codes; `forge test ingest`                                                                                                                                                                                                                                                                                               | forge | M    | F1, F2           | B              |
| F4  | `forge test flakes` (local), `flakes gate` (port of `ci-changed-tests.sh`), `flakes confirm` (port of `flake-report.sh` with the taxonomy), `forge test quarantine` (control-plane's format, `skip-regex`); `flake-check` gate step plus rollout row                                                                                                                                                                                                     | forge | M    | F1, F2           | B              |
| F5  | Instrumented builds: KCL `forge.Tests`, computed default, persistent-env check, one-binary rule; GoBuild flags; Dockerfile `cover` stage; air flags; `cover` release variant plus forge-side refusal on persistent envs; serverkit `/debug/coverage`; `forge test collect` (host, compose, cluster); collect before `env down` / `preview delete`                                                                                                        | forge | L    | 0.1, F2          | B              |
| F6  | `Release.Tests` evidence (machine ledger), old-decoder test; `forge test run`                                                                                                                                                                                                                                                                                                                                                                            | forge | S    | F3               | C              |
| F7  | Scaffold default-on: Taskfile lanes, Vitest block, forge.yaml `tests:`, `ci.yml` upload step, preview workflow e2e plus collect, `release.yml` gate swap; goldens                                                                                                                                                                                                                                                                                        | forge | M    | F3, F4, F5       | C              |
| F8  | `migrations/v1.N/tests` playbook with codemod, `tests-capture` lint rule plus rollout row                                                                                                                                                                                                                                                                                                                                                                | forge | M    | F7               | D              |
| F9  | Docs and skills: `testing/coverage`, `testing/flakes`, `benefits.md`, the `deploy/record` skill (`forge test report --upload` from external CI)                                                                                                                                                                                                                                                                                                          | forge | S    | F7               | D              |

### Phase 2: control-plane store, merge, flakes

| #   | Task                                                                                                                                                                                                                                  | Repo                      | Size | Depends on                 | Parallel group |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------- | ---- | -------------------------- | -------------- |
| C1  | Proto: `TestsService`, `TestsAdminService`, `test_features`; `tests:read` and `tests:write` in forge `pkg/accesstoken` (a small forge PR merged first, then pinned)                                                                   | control-plane (+ forge S) | S    | —                          | A (with F1/F2) |
| C2  | Migrations and `teststore`; `testbucket` adapter with vulnstore's copy-state machine; scope CHECK widened, `tests:read` granted to roles and presets with `deploy:read` (existing tokens untouched); project identity on first upload | control-plane             | M    | C1                         | B              |
| C3  | `RecordTestRun` (digests re-derived, caps, `tests:write`/`deploy:write`, org-scoped) plus `test_ingest` (sandboxed parse via forge's `pkg/testreport`, commit merge, flake events)                                                    | control-plane             | M    | C2, F1 released and pinned | C              |
| C4  | Read API: summary, runs, run, coverage, file coverage, release tests, flaky tests, test; `tests:read`, env-bound tokens, NotFound across orgs                                                                                         | control-plane             | M    | C2                         | C (with C3)    |
| C5  | `test_flake_sweep` (states, opt-in issues and quarantine PRs through the GitHub connection), `test_retention` (the rules in Amendment 1, part B) and the `test_storage_gib` shadow meter, admin RPCs                                  | control-plane             | M    | C3                         | D              |
| C6  | Releases: bind commit reports at `CutRelease` and on later ingest (`deploy_release_tests`); `Promote` refuses a `cover` digest on a persistent env; advertise `release-test-evidence` and `refuses-cover-variant`                     | control-plane             | S    | C3, F5                     | D              |
| F10 | forge hosted: `report --upload`, `collect` uploads, reads (`coverage`, `runs`, `flakes`) from the control plane, `Release.Tests` from the server, `Unimplemented` handled                                                             | forge                     | S    | C1, F3, F6                 | C              |
| P1  | Pin bumps: reliant to the forge with F1–F10, then control-plane in lockstep                                                                                                                                                           | reliant, control-plane    | S    | F1–F10 released            | E              |

### Phase 3: web

| #   | Task                                                                                                          | Repo    | Size | Depends on                   | Parallel group |
| --- | ------------------------------------------------------------------------------------------------------------- | ------- | ---- | ---------------------------- | -------------- |
| W0  | Sync the vendored control-plane proto to a commit with C1                                                     | reliant | S    | C1 merged                    | —              |
| W1  | Data layer (api, wire, context, hooks, fixtures), the Tests nav group and paths, the Overview page            | reliant | M    | W0 (mock transport until C4) | A              |
| W2  | Coverage browser plus annotated source: `LightweightCodeViewer` line tones, GitHub source read, no-repo state | reliant | M    | W1                           | B              |
| W3  | Runs and run detail; Flaky tests and test drawer                                                              | reliant | M    | W1                           | B              |
| W4  | Release Tests tab, Releases column, Overview card, Needs attention items, nav badge                           | reliant | S    | W1                           | B              |

### Phase 4: agents

| #   | Task                                                                                                                                              | Repo    | Size | Depends on             | Parallel group |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------- | ------- | ---- | ---------------------- | -------------- |
| R1  | AskReliant subjects `flaky-test` and `coverage`, plus the buttons on the Flaky and Coverage pages                                                 | reliant | S    | W2, W3                 | A              |
| R2  | Built-in `fix-flaky-test.yaml` (50× race verify, quarantine removal, PR) plus scenario tests for each branch                                      | reliant | M    | F4 released and pinned | A              |
| R3  | Built-in `raise-coverage.yaml` (target check, flake gate, assertion review, PR) plus scenario tests                                               | reliant | M    | F3 released and pinned | A              |
| W5  | Buttons move from AskReliant to `StartRun` with run and PR links, disabled with a stated reason when there is no linked repo or GitHub connection | reliant | S    | R2, R3                 | B              |

### Phase 5: dogfood

| #   | Task                                                                                                                                                      | Repo          | Size | Depends on                       | Parallel group |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- | ---- | -------------------------------- | -------------- |
| D1a | forge, local: lanes and CI shards through capture, `flakes gate` / `confirm` replace its scripts, `flake-check: error`, e2e coverage of the CLI           | forge         | M    | F4, F5, F7                       | A              |
| D2a | control-plane, local: lanes, flake and quarantine scripts → forge, `cover` builds in dev and e2e envs                                                     | control-plane | M    | P1 (or the first pin with F4–F7) | A              |
| D3a | reliant, local: lanes, `test:flake` and flake report → forge, instrumented dev stack, Vitest coverage                                                     | reliant       | M    | P1                               | A              |
| D1b | forge, hosted: upload from CI and main, opt-in issues and quarantine PRs                                                                                  | forge         | S    | D1a, C5, F10                     | B              |
| D2b | control-plane, hosted: `ci-full-main.sh` uploads; two-week parity against `refs/notes/ci-full`, then control-plane's release-cut script reads the product | control-plane | M    | D2a, C6, F10                     | B              |
| D3b | reliant, hosted: upload, opt-in issues and quarantine PRs                                                                                                 | reliant       | S    | D3a, C5, F10                     | B              |
| D4  | Delete the ported scripts in each repo once its hosted step has run clean for two weeks                                                                   | all three     | S    | D1b, D2b, D3b                    | C              |

### Phase 6: follow-ups (each optional)

| #   | Task                                                                                                                     | Repo                    | Size |
| --- | ------------------------------------------------------------------------------------------------------------------------ | ----------------------- | ---- |
| X1  | `delivery_policy.test_gate` (audit default, rollout row; forge for self-hosted envs, control-plane in `Promote`)         | forge + control-plane   | M    |
| X2  | A report-only GitHub check run and PR comment with diff coverage and new flakes (always a neutral conclusion)            | control-plane           | S    |
| X3  | Frontend e2e coverage: the `forgeCoverage()` Vite plugin, the Playwright fixture, the frontend `cover` variant           | forge                   | M    |
| X4  | Instrumented servers in other languages for adopted apps (coverage.py, `c8` for Node) through `forge test collect` hooks | forge                   | M    |
| X5  | `source: tests` trigger for opt-in agent automation                                                                      | control-plane + reliant | S    |
| X6  | Org-wide Tests rollup; per-test analytics in the org's ClickHouse                                                        | reliant + control-plane | M    |

**Merge order for F7.** The scaffold runs forge commands in a project's
Taskfile and CI, and `TestScaffoldTemplatesRunOnlyReleasedForgeCommands`
fails a template that runs a command no released forge has. So F7 merges only
after a forge release that contains F2–F6 and F5b (`forge test collect`,
split out of F5), followed by a refresh of
`internal/cli/testdata/released_commands.txt`.

**Critical path to "a hosted project shows real coverage and flakes":** F1 →
F2 → F3 → F10 → P1, with C1 → C2 → C3 → C4 running alongside, then W0 → W1.
F5 (instrumented builds) runs beside that path and is not on it: unit and
integration coverage come first, and e2e coverage joins when F5 lands. Phase 1
stands alone, so a self-hosted project gets results, merged coverage and flake
detection with no control-plane change. The dogfood steps that only need the
local part (D1a, and D2a and D3a once pinned) do not wait for phase 2.

## 9. Decisions for the owner

> All five are settled. See [Decisions](#decisions-owner-2026-11-20); 3 and 5
> were decided differently from the recommendations below.

1. **Where Tests lives in the nav** ([3.1](#31-where-it-lives)).
   (a) A Tests group after Deployments (Overview, Coverage, Runs, Flaky
   tests), plus a release tab and an Overview card. (b) A release tab only.
   (c) Merged with Security into a "Quality & security" group.
   **Recommend (a).** Most runs never become a release, and forge itself has
   no releases. Coverage and flake triage are daily work. Security has a
   different audience and was just settled.

2. **What a preview env runs, now that instrumented previews are the
   default.** (a) The preview runs the release's `cover` variant (the same
   commit, built with `-cover`, recorded on the release, never promotable).
   (b) The preview runs the shared digest, and coverage comes only from a
   separate ephemeral e2e env the preview workflow creates. (c) The preview
   runs both side by side.
   **Recommend (a).** It is the settled default with the least machinery: one
   env, one e2e run, coverage for free. The cost is that the preview tests a
   binary a few percent slower than the one prod will run. Promotion is
   untouched, because prod still runs the digest staging ran. (b) doubles the
   envs per PR. (c) doubles the workloads and makes it unclear which one the
   e2e run hit.

3. **Source in the coverage browser.** (a) Read source from the linked GitHub
   repo at that commit, on the viewer's behalf, and store none. (b) forge
   uploads the covered files with the report. (c) No source; show line
   numbers and hits only.
   **Recommend (a).** Storing customers' source in the control plane is a new
   class of data, with its own retention, access and breach story. (a) needs a
   linked repo and falls back to (c) without one. (b) works for any host, but
   it makes the control plane a source store.

4. **Scopes.** (a) A new `tests:read` (granted on day one to every
   `deploy:read` holder) and a new narrow `tests:write` (also accepting
   `deploy:write`). (b) Reuse `deploy:read` and `deploy:write`. (c) A new
   `tests:read`, and writes under `deploy:write` only.
   **Recommend (a).** A PR's CI job should be able to upload test results
   without holding the authority to cut releases. Failure output can quote
   config and request bodies, so reads are deliberately grantable apart from
   deploy history. This mirrors `vuln:read` and `sourcemaps:write`.

5. **Retention and billing.** (a) Plan retention `{free: 30, paid: 180}` days
   for raw reports, per-file coverage and case rows. Summaries for the default
   branch kept 400 days, and anything a retained release references kept with
   the release. No meter at launch. (b) The same, plus a `test_storage` meter
   from day one. (c) A flat 90 days for everyone, no plan difference.
   **Recommend (a).** The caps bound storage. Phase 0 gives real sizes before
   a meter is designed, and the free tier still shows a month of trend and
   flakes.

Two smaller calls go the recommended way unless vetoed:

- **The `forge test` name** comes back as a group that never runs a suite of
  its own: `run` only runs the project's Taskfile lanes ([1.7](#17-cli)). The
  alternative is `forge ci test …` with the same subcommands.
- **Quarantine PRs and flake issues are opt-in for customers**
  (`tests.flakes`), and on for our three repos, which follows the owner's
  "flaky = delete" policy. This is the report-only rule applied to flakes.

## Decisions (owner, 2026-11-20)

The owner took the recommended option on decisions 1, 2 and 4 and on both
smaller calls, and then decided the two reopened questions (3 and 5) after
[Amendment 1](#amendment-1-coverage-source-and-retention-2026-11-20). All of
these are settled, and implementation follows them:

1. **Nav: (a).** A **Tests** group after Deployments, with Overview,
   Coverage, Runs and Flaky tests, plus a Tests tab on release detail, a Tests
   column in the releases list and a Tests card on the project Overview
   ([3.1](#31-where-it-lives)).
2. **Previews: (a).** A preview runs the release's `cover` variant. It is the
   same commit built with `-cover`, recorded on the release, and never
   promotable to a persistent env ([1.3](#13-instrumented-builds-integration-and-e2e-coverage)).
3. **Coverage source: GitHub only, nothing stored.** The owner: "Just do
   GitHub for now, if we find shortcomings we can adjust later. Let's not store
   the excess data." The coverage browser fetches each file from the project's
   linked GitHub repository when it is viewed, by the git blob id forge sends
   with every report. Without a linked GitHub repo, or for a file whose blob is
   not in any commit, the browser shows line numbers and hit counts only. No
   source upload exists, not even as an opt-in, and our own repos use the same
   path ([Amendment 1, part A](#a-where-the-coverage-browser-gets-source)).
4. **Scopes: (a).** A new `tests:read`, granted on day one to every
   `deploy:read` holder, and a new narrow `tests:write`. `RecordTestRun` also
   accepts `deploy:write` ([2.4](#24-tenancy-and-grants-fails-closed)).
5. **Retention: one simple rule set, under the org's global storage tier.** The
   owner: "I don't love all of our scattered storage tier policies. Can we have
   one global storage tier? Or no? I prefer simple for now." A separate design
   for one global storage tier has started. Tests uses the fewest knobs that
   work, the same for every org:

   - the default branch keeps its newest reports in full, plus a daily
     summary;
   - every other branch keeps only its latest report, deleted a day after the
     branch merges or closes;
   - a release keeps its report while the release is kept.

   Tests has no allowance of its own and no free/paid split. Its storage
   counts against the org's global storage tier once that exists. Until then
   it is tracked and not charged ([Amendment 1, part B](#b-retention)).

The two smaller calls are settled too:

- **`forge test`** comes back as a verb group that never runs a suite of its
  own: `run` only runs the project's Taskfile lanes ([1.7](#17-cli)).
- **Quarantine PRs and flake issues are opt-in for customers**
  (`tests.flakes`) and on for forge, control-plane and reliant
  ([1.5](#15-results-and-flaky-tests), [section 6](#6-dogfood-forge-control-plane-reliant)).

### Decision (2026-11-21): service names

The owner settled the control-plane service names: **`TestsService`** in
`proto/services/tests/v1/` and **`TestsAdminService`** in
`proto/services/tests_admin/v1/`. forge requires a service's name to match
its proto directory, so the name follows from the directory and is not
reopened ([2.3](#23-api)).

### Decision (2026-11-21): nav badge count

The owner decided that the Tests nav badge and the Needs-attention
"suspected" row follow the project's flake policy. That is the switch that
files flake issues and opens quarantine PRs at `suspected`: forge.yaml
`tests.flakes.issues: true` or `tests.flakes.quarantine: pr`.

- **Policy on** (our three repos, and any project that opts in): the badge
  counts `suspected` plus `flaky`, and Needs attention lists suspected tests.
- **Policy off** (the customer default): the badge counts `flaky` only, as
  [3.1](#31-where-it-lives) proposed, and there is no suspected row.

forge sends the policy with each upload, and `GetTestSummary` returns it
(control-plane#3331), so the web reads the project's own setting.

## Amendment 1: coverage source and retention (2026-11-20)

The owner reopened decisions 3 and 5 with these questions. On source: "What
does gocov do? Should we store the full code file, or snapshots? Doesn't a
coverage report contain enough detail in the first place? Or maybe we upload
surrounding lines?" On retention: by day or last N, non-default branches kept 1
day, "could do better on retention", and perhaps fitting under the 10 GB
free-tier quota. This amendment records the research and the result. The
owner's decisions are [above](#decisions-owner-2026-11-20).

### How Go coverage tools work: no format carries source

Every Go coverage format before the final HTML holds **positions and counts,
never source text**. Each tool reads the source from disk when it renders, and
expects that source to be the exact source the tests ran against.

| Tool / format                                     | What it holds                                                                                                                                       | Where the source comes from                                                                                                                    | Source                                                                                          |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| Text profile (`-coverprofile`, `covdata textfmt`) | `mode: set\|count\|atomic`, then one line per block: `importpath/file.go:startLine.startCol,endLine.endCol numStmt count`                           | Nowhere. It names the file by **import path**, not repo path                                                                                   | [`x/tools/cover/profile.go`](https://github.com/golang/tools/blob/master/cover/profile.go)      |
| covdata (`GOCOVERDIR`)                            | A meta file (package, function and file names, and units `{StLine, StCol, EnLine, EnCol, NxStmts}`) plus counter files                              | Nowhere. `strings` on the meta file showed only names                                                                                          | [go.dev/doc/build-cover](https://go.dev/doc/build-cover), `src/internal/coverage/defs.go`       |
| `go tool cover -func` / `-html`                   | Reads a text profile                                                                                                                                | Resolves import paths with `go list -json` (`findPkgs`, `findFile`) and `os.ReadFile`s each file at render time                                | `src/cmd/cover/func.go`, `html.go`                                                              |
| `axw/gocov convert`                               | JSON: packages → functions `{Name, File, Start, End}` → statements `{Start, End, Reached}`, as **byte offsets**, with `File` an absolute local path | Parses the files on disk with `go/parser` **at convert time**. It embeds no source                                                             | [`gocov/convert/convert.go`](https://github.com/axw/gocov/blob/master/gocov/convert/convert.go) |
| `matm/gocov-html`                                 | Reads gocov JSON                                                                                                                                    | Reads each `File` from disk at render time and **embeds the source in a self-contained HTML file**. This is the only stage that carries source | [`pkg/themes/report.go`](https://github.com/matm/gocov-html/blob/master/pkg/themes/report.go)   |

We ran three experiments (go1.27, a one-file module):

- With the file missing, `go tool cover -html` fails with `can't read …`,
  `gocov-html` fails, and `gocov convert` silently prints `{"Packages":null}`
  and exits 0.
- With two comment lines added to the file after the test ran, `go tool cover
-func` reported `a.go:5: Abs 50.0%` instead of `a.go:3: Abs 66.7%`, and
  exited 0.

**Shown against the wrong source, coverage is silently wrong.** Whatever
supplies the source must supply the exact bytes the tests ran on.

**So does a coverage report hold enough detail?** It holds enough for every
number: the percentage per lane, per package, per file and per line, and diff
coverage. It does not hold enough to show code. Without source, the file view
is line numbers with hit counts.

**What the hosted products do:**

- **Codecov stores no source.** At view time it fetches the file from GitHub,
  GitLab or Bitbucket (including on-prem) with the viewer's OAuth token. If
  access is revoked, the page shows an error. Some raw uploads (gcov) contain
  source and are archived for one month, which can be turned off. Processed
  reports are kept indefinitely and never contain source
  ([about.codecov.io/security](https://about.codecov.io/security/)).
- **Coveralls stores no source by default.** Each uploaded file entry is
  `{name, source_digest (MD5), coverage[], branches?, source?}`. Coveralls
  fetches source live from the git host. `source` is required only for manual
  repos on Enterprise ([api-introduction](https://docs.coveralls.io/api-introduction),
  [api-jobs-endpoint](https://docs.coveralls.io/api-jobs-endpoint)).
  goveralls nonetheless sends full source by default (`-uploadsource=true`,
  [`goveralls.go`](https://github.com/mattn/goveralls/blob/master/goveralls.go)).
- **SonarQube and SonarCloud store source, for the latest analysis only.** The
  previous analysis's source is removed when a new one finishes. Historical
  snapshots are thinned (on Server: one per day after a day, one per week after
  a month, then monthly, then version events only, all gone after 5 years).
  Inactive branches are deleted after a configurable number of days, and
  SonarCloud keeps PR data 4 weeks
  ([housekeeping](https://docs.sonarsource.com/sonarqube-server/instance-administration/system-functions/housekeeping),
  [project data lifecycle](https://docs.sonarsource.com/sonarqube-cloud/managing-your-projects/project-data-lifecycle)).

**The git blob id makes exact source cheap to identify.** It is `sha1("blob
<len>\0" + content)`, which `git ls-files -s` lists for free. If forge sends
`path → blob id` for each covered file, the source is pinned exactly:

- **Fetch** uses GitHub's `GET /repos/{o}/{r}/git/blobs/{sha}` (up to 100 MB,
  "Contents: read", works with installation tokens), and the result is
  immutable and cacheable. An installation token gets 5,000 requests an hour,
  rising to 12,500, or 15,000 on Enterprise Cloud
  ([blobs](https://docs.github.com/en/rest/git/blobs),
  [rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)).
- **Mismatch detection.** A file with uncommitted changes has a blob id that
  is in no commit, so the UI says "source not available for this version"
  instead of drawing wrong highlights.

### A. Where the coverage browser gets source

**Decided: fetch from the linked GitHub repo at view time, store none.**

- **What forge sends.** Every `forge.test.coverage/v1` document carries a
  repo-relative path and a git blob id for each covered file, next to the
  import path. forge gets them from `git ls-files -s`, or from `git
hash-object` for a file with uncommitted changes. Blob ids are always sent,
  and there is no knob.
- **How a file is shown.** A new reliant RPC, `GetCoverageSource(project,
blob_id)`, reads the blob through the project's GitHub connection (`GET
/repos/{o}/{r}/git/blobs/{sha}`). It serves only blob ids that appear in a
  retained report of that project, so `tests:read` cannot be used to read
  arbitrary repository files. A blob is immutable, so the response can be
  cached briefly by blob id. The viewer must hold `tests:read` on the project.
- **When there is no source.** There are four cases:

  - no linked GitHub repo (a local-only folder, GitLab, Bitbucket, a GitHub
    Enterprise Server we cannot reach);
  - a blob in no commit (the run had uncommitted changes);
  - a blob GitHub no longer has;
  - a revoked connection.

  In every case the file view shows line numbers and hit counts, with one line
  saying why. The numbers (per lane, per package, per file, per line and diff
  coverage) do not need source, so they are unaffected. The UI never draws
  highlights over source that does not match the blob, because the experiment
  above shows that is silently wrong.

- **Not stored, anywhere:** no source snapshots, no hunks, no upload path. If
  GitHub-only proves short (the owner: "if we find shortcomings we can adjust
  later"), the blob ids already in every report are what a later fetcher for
  GitLab or Bitbucket, or an upload path, would key on. Old reports would need
  no change.

The options that were weighed, kept for the record: (a) fetch at view time,
**chosen**; (b) a content-addressed snapshot of the covered files, deduplicated
per org by blob id; (c) only the lines around uncovered or changed lines; (d)
line numbers and hit counts only, which is now (a)'s fallback.

Measured on `origin/main` on 2026-11-20, a snapshot of the covered Go source
would have been:

| Repo          | Snapshot size | New or changed blobs in 30 days |
| ------------- | ------------- | ------------------------------- |
| forge         | 8.1 MB gzip   | about 38 MB gzip                |
| control-plane | 4.9 MB gzip   | about 25 MB gzip                |
| reliant       | 5.6 MB gzip   | about 36 MB gzip                |

With (b) we would have held customer source, which needs encryption,
purge and a breach story. That is the data the owner chose not to keep.

### B. Retention

**Decided: one rule set, the same for every org, under the global storage
tier.**

The research behind it:

- control-plane has **no org-wide storage quota today**. Each stored product
  has its own meter and allowance (`ck_cp_rub_kind` in
  `control-plane/db/migrations/20261106085053_pricing_v2_shadow_kinds.up.sql:33-43`).
- The 10 GB users see is **observability's 10 GiB of stored ingest per org
  per month** (`internal/plansconfig/pricing.yaml:449-454`).
- Other caps are separate:
  - static site: 1 GiB;
  - registry: 5 GiB, shadow-billed;
  - managed Postgres disk: nothing included;
  - object storage: no meter.

That scatter is what the global storage tier design replaces. Tests adds no
new per-product cap to it.

**The rules** (`TestsService` retention, applied by the daily `test_retention`
worker):

| What                        | Kept                                                                                                                                                                                                            |
| --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Default branch**          | The newest **50** commit reports in full (results, per-file coverage, blob ids), and a **daily summary** (totals, per-lane and per-package coverage) for **400 days**, the platform's existing retention cap    |
| **Any other branch or PR**  | Only its **latest** report. It is deleted **1 day** after the branch merges or closes, or after **30 days** with no upload, which counts as closed. Merge and close come from the GitHub connection when linked |
| **Releases**                | The release commit's report, for as long as the release is retained (`deploystore.RetainedReleaseIDs`: live releases, rollback targets, the newest per project)                                                 |
| **Flake events and states** | 400 days. Rows only, about 100 bytes each, which is what scores and history read                                                                                                                                |
| **Raw uploads**             | Deleted once ingest succeeds. The normalised documents are the record                                                                                                                                           |

**Which rule governs a report.** A report is immutable and keyed by its
commit, not by the branch it was uploaded under. It is never copied, moved or
duplicated; only the rule that governs it changes.

- A commit is governed by the default-branch rule as soon as it is reachable
  from the default branch. The control plane learns that from the GitHub
  connection (a push to the default branch, a merged PR), or from a
  default-branch upload that names the commit. Its report then counts toward
  the newest 50 like any other default-branch report.
- The other-branch rule only ever deletes reports of commits that are **not**
  reachable from the default branch.
- A merge commit or a fast-forward makes the branch's head reachable, so its
  report passes to the default-branch rule. A squash or rebase merge creates a
  new commit, which gets its own report from the default branch's CI. The
  branch's report of the pre-merge commit stays unreachable and is deleted a
  day after the merge, under the branch rule.

**Knobs.** There is one, `tests.retention.default_branch_reports` (default
50), and a project may only lower it. Every other number is a constant: no
free/paid split, no per-plan block in `pricing.yaml`, no separate Tests
allowance. The org's tier decides how much an org may store in total.

**Storage and the tier.**

- Tests writes its stored bytes per org to a `test_storage_gib` meter. It is
  **tracked, not charged**, the registry's shadow precedent.
- When the global storage tier exists, that meter counts against it under the
  tier's rules.
- Tests never refuses an upload because of storage. What happens at a full
  tier is the tier design's to decide. If it asks products to shed data, Tests
  sheds default-branch reports beyond the newest 10 first, and never
  summaries, releases or open branches' latest reports.

**Sizes.** These were measured on 2026-11-20 on a fifth of forge's packages
and scaled. Phase 0 confirmed them: a commit's report (unit, integration and
one server) is 2 to 4 MB gzipped raw, deleted after ingest, and about 1 MB
gzipped as merged per-file blocks
([Amendment 2](#amendment-2-phase-0-results-2026-11-21)):

- **One stored commit report** is about **1 MB** for forge (about 245
  packages: a 0.8 MB gzip cover profile plus normalised results and about 50 KB
  of Postgres rows) and about **120 KB** for a typical 30-package service.
- **Pass rows are implied**, not stored (2.1), which is what keeps the
  Postgres share small.

| Project                                                      | Default branch (50 reports) | Open branches (latest each) | Summaries (400 days) | Total           |
| ------------------------------------------------------------ | --------------------------- | --------------------------- | -------------------- | --------------- |
| forge-sized (about 40 main commits a day, about 30 open PRs) | about 50 MB                 | about 30 MB                 | about 12 MB          | **about 90 MB** |
| Typical (about 5 commits a day, a few open PRs)              | about 6 MB                  | about 0.5 MB                | about 2 MB           | **under 10 MB** |

**Tasks changed by this amendment:**

- **C2** adds blob-id and repo-path columns and the non-pass-only case table.
- **C5** implements these rules and the `test_storage_gib` shadow meter. It
  drops the plan-catalog `tests.retention_days`.
- **F3** puts blob ids and repo paths in `forge.test.coverage/v1`.
- **New W2b** (reliant, S): `GetCoverageSource` over the GitHub connection,
  restricted to blob ids in a retained report, with the line-numbers fallback
  and its reasons.
- No source-upload task exists.

## Amendment 2: phase 0 results (2026-11-21)

Phase 0 (tasks 0.1 and 0.2) measured the design on 2026-11-20/21 and
confirmed it, with the eight amendments below. The owner settled them, and
they are folded into the body; the decisions above are unchanged.

**Setup.** All three repos pin `toolchain go1.27.4`. Snapshots of `origin/main`
(forge `d75f656d9`, control-plane `8bcf5bf9b`, reliant `5830ffe07`), with
every fifth package of `go list ./...` minus `gen/`: forge 49 of 241,
control-plane 76 of 380, reliant 73 of 415. Every run was the repo's own
`task test:short -- -json [flags] <pkgs>` on the shared build cache, on a
48-CPU host shared with other agents, so the conclusions rest on paired,
interleaved re-runs.

**Coverage cost, `-short` unit lanes** (wall seconds):

| Run                                | forge (49) | control-plane (76) | reliant (73) |
| ---------------------------------- | ---------- | ------------------ | ------------ |
| plain, cold build                  | 42.0       | 55.9               | 145.6        |
| plain, warm (all cached)           | 9.2        | 2.5                | 5.6          |
| `-coverprofile`, cold build        | 87.7       | 85.3               | 300.3        |
| `-coverprofile`, warm (all cached) | 4.3        | 2.8                | 5.1          |
| `-coverpkg=<module>/...`, cold     | 241.2      | 101.3              | 201.6        |

Paired, interleaved re-runs showed that the apparent slowdowns under
`-count=1` and `-race` were load (reliant's shared embedded Postgres, noise
under `-race`), not instrumentation: summed package time with cover
against plain was 0.77 (forge) and 0.86 (control-plane), and `set` against
`atomic` was within noise.

**Instrumented server** (reliant `server api`, plain against
`-cover -covermode=atomic`, 1,000 interleaved requests per endpoint): binary
425.7 against 438.3 MB (+2.9%), RSS 263 against 274 MB (+4%), p50 and p90
within noise (ListProjects p50 0.93 against 0.94 ms), SIGTERM exit 0 with
counters written. One `covmeta` file of 1.15 MB gzipped covers 315 packages
and 205k statements, 21% of them generated.

**Unit plus server merge** (six reliant packages): unit 80.8%, server 48.4%,
union 82.7%. The union equals Go's `covdata merge`, block for block.

**Flake data:** 77 `flake:suspected` and 103 `flake:removed` issues across the
three repos, against about 500 control-plane `ci:full` runs and about 1,000 to
1,100 forge and reliant push-to-main runs in 30 days. There was no per-test
run history to read, so `n` could only be a lane's run count.

**The amendments:**

1. **Cache replay applies to the cached `-short` lanes only** (forge's
   merge-tier shards, control-plane's `task ci`, reliant's merge gate), where
   capturing coverage is close to free. A `-count=1` lane such as `task test`
   never replays ([1.2](#12-producing-it-the-projects-own-lanes)).
2. **`-covermode=atomic` everywhere:** scaffolded lanes, `GoBuild` and the
   Dockerfile `cover` stage. Hit counts sum only because every input is
   `atomic` or `count`; a `set` profile is accepted and merged as boolean
   ([1.2](#12-producing-it-the-projects-own-lanes), [1.4](#14-merging-and-accumulating-across-lanes)).
3. **`-coverpkg=<module>/...` stays opt-in** (`tests.coverage.packages:
module`), and capture deduplicates it before anything is stored: raw
   profiles repeat each block 18 to 31 times (105, 228 and 457 MB on the
   samples) ([1.2](#12-producing-it-the-projects-own-lanes)).
4. **Counters are written on `os.Exit`** and on return from `main`. Only
   SIGKILL, an unrecovered panic or a fatal signal lose them. Pull stays
   preferred for long-lived pods ([1.3](#13-instrumented-builds-integration-and-e2e-coverage)).
5. **No merge across builds.** covdata is grouped by `covmeta` hash and two
   builds of one package are never merged; disagreeing statement counts for
   one block key are a source mismatch and are refused (`exit 2`). Go's own
   `covdata merge` accepted two builds and reported 72 statements for a
   36-statement package ([1.4](#14-merging-and-accumulating-across-lanes)).
6. **`flaky` is two or more confirmed events on distinct commits** in 30
   days. The Wilson bound is a displayed ranking with its `n`, never a
   trigger: 0.02 needs 5, 17 and 29 flaky commits at 100, 500 and 1,000 runs,
   and the worst known flake had 5 (0.002). The denominator is completed
   default-branch runs of the lane in which the test ran, excluding cancelled
   runs. Per-test outcomes `(test id, lane, commit, run id, attempt, status)`
   are recorded per run. `suspected` is the actionable state for our repos'
   delete-on-first-flake policy ([1.5](#15-results-and-flaky-tests), [2.1](#21-store)).
7. **`host` verdict and correlated grouping**, matched before scoring. Host
   events (network and download errors, `toomanyrequests`, ENOSPC and quota,
   address in use, docker CLI timeouts, `panic: test timed out` under load)
   never score and are reported as host health. Three or more failures in one
   run sharing a package, harness or signature are one infra event (verdict
   `correlated`, grouped by `infra_group`), which
   replaces `broad`. At least 30 of the 77 suspected issues had a host or
   harness cause, and host failures came 2 to 8 tests at a time, so `broad`
   never fired ([1.5](#15-results-and-flaky-tests), [2.2](#22-ingest-merge-and-flake-detection)).
8. **Generated code is excluded by default**, now including `*.pb.go`, and
   reported on its own line: 21% of reliant's API server statements
   ([1.4](#14-merging-and-accumulating-across-lanes)).

**Not covered by phase 0:** the `/debug/coverage` prototype, SIGTERM
collection in k3d, Vitest LCOV in the merge, a cold-CI number with an empty
build cache, and the build-cache growth from the cover variant. The first
three are checked in F5 and F1.

**Tasks changed by this amendment:**

- **F1** adds block-key dedupe, `covmeta`-hash grouping, the boolean `set`
  merge, the `host` and `correlated` verdicts, and the flake rate as a
  ranking.
- **F2** deduplicates `-coverpkg` profiles at capture.
- **F5** and **F7** pass `-covermode=atomic` everywhere.
- **C2** adds `test_run_packages`, `test_sets` and `test_infra_events`, and
  keys case rows by attempt.
- **C3** matches host signatures and groups correlated failures before it
  writes flake events.
