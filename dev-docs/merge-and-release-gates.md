# The merge gate and the release gate

**Target: ≤5 minutes wall time; slow suites gate releases, not merges (owner,
2026-10-28).** The merge gate runs what the diff can affect. When the rules cannot
scope a change (`go.mod`, `package.json`, config, an unclaimed path) it runs the
whole Go `-short` lane and electron's tests (5m13s and 40s measured on a loaded
host), and for a full web fallback `tsc -b` over every web source; the whole vitest
suite (1033 files, ~22 min loaded) is the one lane left to the release gate's
`task test`, noted in the summary. It also runs electron's tests when electron reads
a changed file, and the docs-generate no-op check when a docs input changed. A
deleted Go package compiles the module instead of forcing every lane.
`scripts/merge-gate-budgets.json` pins step budgets; an overrun prints
**GATE-HEALTH** (a gate-speed regression to fix, not PR blame).

reliant has two local gates. The **merge gate** runs before every merge and
judges only what the change can affect. The **release gate** runs on main
before a release and judges everything. The split is deliberate: **speed was
chosen over catching everything before the merge.** The merge gate aims at
**five minutes of wall time** for a typical PR. Whatever it leaves out runs in
the release gate, and a release can only be cut from a commit the release gate
recorded green.

This mirrors control-plane's `task ci` / `task ci:full`
(`control-plane/docs/local-gate.md`). The one rule set for "what can this diff
affect" is reliant's own `tools/test-affected` (`dev-docs/testing-affected.md`).
Neither gate keeps a second copy of it.

| Tier | Command | What it runs | When |
|---|---|---|---|
| **merge** | `task ci:merge` (`scripts/merge-gate.sh`) | the affected tests (`-short`, cached, no `-race`, a per-test time budget) of the changed packages and their **direct** importers, and the vitest files within two imports of a changed module; a compile (no link) of every package whose build contains a change; the web `tsc -b --force` (checkout-local state) when a web file changed; forge lint's own lanes over the changed directories (no golangci-lint, no buf lint); migration-safety when a migration or forge.yaml changed; electron's tests when electron reads a changed file; the whole lane when the rules cannot scope a change; and the forge-generate and docs-generate no-op checks only when one of their inputs changed | before **every merge**. The merge queue runs it on the squash it is about to land |
| **release** | `task release:gate` (`scripts/release-gate.sh run`) | everything: unscoped `forge lint` (golangci over the whole module, buf, the whole-project lanes, the frontends), `task generate:check`, `task generate:docs:check`, `go build`, the web `tsc -b`, `go vet ./...`, `task test` (Go `-race -count=1` without `-short`, so every slow test runs, plus web and electron in full), `task test:integration` and `task test:e2e` | on a commit of **main**, before a release. `task release:tag` refuses a commit without a green record |

## The merge gate

```sh
task ci:merge                    # your branch, against its merge-base with origin/main
task ci:merge -- --base <rev>    # a stacked branch
```

It holds **one** host heavy slot for the whole run (`forge slot run`), and every
phase nests inside that slot, within a CPU budget (below). It prints one row per phase:

```
### merge-gate: PASSED          (#1427's own merge, load ~50 on 32 CPUs)

| phase | result | seconds | started +s |
|---|---|---|---|
| heavy slot (queue) | - | 86 | - |
| plan | PASS | 28 | 1 |
| npm-tools-protoc-gen-es | PASS | 10 | 32 |
| generate | PASS | 352 | 49 |
| docs | PASS | 136 | 58 |
| build | PASS | 132 | 58 |
| test | PASS | 261 | 58 |
| lint | PASS | 271 | 156 |
```

The first row is the time spent **queued** for a slot. That cost comes from the
host, not from the gate: other agents' builds hold the slots. The five-minute
target is measured on the `wall:` line, which starts once the slot is held.

The phases:

1. **plan**: `tools/test-affected plan -json`, which decides everything below.
2. **generate** runs only if the plan's forge-generate lane is selected. It runs
   `task generate:forge`, the same command `task generate:check` runs, followed
   by `forge lint --conventions`. When every reason is Go code that a
   `forge.yaml` generate hook's generator compiles, and nothing forge itself
   reads changed (the plan's `hooks_only`), it runs only those hooks' commands.
   That covers most Go-only PRs, because `toolconfiggen` compiles half the
   module. It is the same check in seconds instead of the 170-350s a whole
   forge generate takes on this host. What it does not do is forge's
   `go mod tidy`; the release gate runs the whole generate. Your branch must arrive with its generated
   output committed. In the merge queue's `--amend` mode, a regenerate that
   changes only generated paths is folded into the squash. That happens when
   merging main left a generated file a step behind. A regenerate that changes
   any other file fails the gate.
3. **docs** runs only if the plan's docs-generate lane is selected. It runs
   `task generate:docs` under the same no-op rule.
4. **build**, **test**, **typecheck**, **lint** and **migsafety** then run at
   the same time (after the generators, when they may rewrite the tree; below):
   - **build** compiles, without linking, every package whose build contains a
     changed package (the plan's `go.build`, at any distance), or `./...` when
     the Go lane is full: `go list -export`, which compiles each package for
     its export data and fails naming any that does not compile, main
     packages included. `go build ./...` also LINKED every main package (the
     server, the CLI, a dozen generators): 41s with every package already
     compiled, against 6s for `go list -export ./...` (2026-10-26). Link
     errors are the release gate's `go build`.
   - **test** is `tools/test-affected run -plan <plan.json> -lanes go,web,electron
     -test-budget auto`. It runs the plan the plan phase wrote (re-planning
     loaded the whole package graph again: 17-141s), so the changed packages
     and the packages that import one by name, not the whole
     reverse-dependency closure, and the vitest files within two imports of a
     changed module (below). A Go test the change adds or edits that runs past
     the per-test budget fails it (below). When `--amend` folded a
     regenerate into HEAD first, the gate plans again (`plan-2`) and build
     and test run that plan, since regenerated code can change what the diff
     reaches. Saved plans require schema, exact base/head and a digest of
     tracked and non-ignored untracked input bytes. Reuse after a source edit,
     head/base change or incomplete JSON is refused. Selection, installs,
     build/tests and closing verification all use the post-generation plan;
     newly expanded generator obligations require a rerun rather than crediting
     a narrower earlier generation result. The atomic control-plane generation
     hook always executes: pins alone cannot identify installed plugin/runtime
     bytes, so its former generated-output result cache is not reused.
   - **typecheck** is `scripts/web-typecheck.sh`: frontends/web's
     `npm run typecheck -- --force` (`tsc -b --force`, which
     includes `tsconfig.test.json`, so test files are type-checked). It runs
     when the plan selects the web lane or the diff touches anything under
     `frontends/web/`, or when a regenerate rewrote a web file. The web test lane cannot stand in for it: vitest strips types
     without checking them, and a changed module no test imports selects no
     test. #1556 merged a test type error because the gate had no such phase.
     Each file is type-checked once: `tsconfig.test.json` holds only the
     tests and references the composite app and gen projects, reading their
     emitted declarations. The build info and declarations live in
     `frontends/web/node_modules/.tmp/`, which the gate's `git clean -fdq`
     keeps (it is ignored) and `npm ci` drops with the rest of
     `node_modules`. The script detaches hard-linked state before invoking
     tsc and never restores or publishes another checkout's state. `--force`
     prevents the solution-builder's timestamp shortcut from skipping a
     backdated source edit or a copied installation. Dependency/config hashes
     alone do not make cross-checkout state safe: tsc can declare the project
     current before comparing source content. These correctness repairs
     invalidate the earlier shared-cache timing claims.
   - **lint** is forge lint's own lanes, named (`--contract --conventions
     --generated-drift --go-markers --proto-markers --create-nullability
     --computed-fields --read-only-fields --db-grants --column-markers
     --closed-sets --tracing --guarded-fields`), with `--scope <every changed
     directory>`, a pin move included. golangci-lint, buf lint, the
     whole-project lanes and the deploy lanes run in the release gate's
     unscoped `forge lint`: on one loaded host the same two-directory scope
     took 331s with every default lane and 41s with these (golangci-lint is
     the bulk), and a pin move made the merge queue's lint unscoped, up to
     2735s. A pin bump's regenerated files are in its diff, so they are in
     scope; what a new version breaks elsewhere in the module is the release
     gate's to find.
   - **migsafety** is `forge lint --migration-safety`, and runs only when a
     migration changed.
   - **migorder** is `go run ./tools/test-affected migration-order`, and runs
     only when a migration changed. It fetches `origin/main` and fails a
     migration versioned at or below main's newest, which golang-migrate would
     never apply (reliant#1399). It fetches because migsafety reads the local
     `origin/main` as last fetched, and a branch reviewed while main moved
     passes it against a stale head.

Where a regenerate becomes the tree the other phases judge, the generators run
FIRST, in place, and build, test, typecheck, lint and migsafety start only once
they are done. That is the merge queue's `--amend` mode: a regenerate that
changes only generated paths is amended into the squash before anything builds
or tests it, so a generated file the merge with main left a step behind (a
stale mock) is never compiled or tested in its stale form. It is also a dirty
tree (a local run with uncommitted edits), because a scratch checkout of HEAD
would not see your edits. Ordering costs generate's wall time only when
generate is selected at all.

On a clean tree without `--amend` (`task ci:merge` on a committed branch) a
regenerate cannot change what is judged: any change fails the generate phase.
There, generate and docs run in scratch checkouts of HEAD, at the same time as
the other phases.

### A skip is not a pass

The gate fails closed on a phase that never completed. Every phase records
itself in `$MG_LOGDIR/expected` before it launches and leaves a `<phase>.row`
only when it finishes. The last step, `go run ./tools/test-affected gate-verify`,
compares what the plan **obliges** (build, test, plus `generate`, `docs`,
`migorder`, `typecheck`, `migsafety`, `lint` as the plan and the diff select
them) and what the gate **started** against the rows. A phase with no row
(queued for a slot and cut off, cancelled, timed out, its tool missing) fails
the gate and is named: `FAILED: docs never completed (no result row; ...)`.
The verdict is also written to `$MG_LOGDIR/verdict`. A missing `jq` or an
unparsable `plan.json` aborts the gate, because every lane would otherwise read
as "not selected". reliant#1393 merged with docs-generate still queued; this is
the check that stops that. The merge queue posts `merge-gate` SUCCESS only when
the verdict is `OK`, and posts a FAILURE status carrying it otherwise.

### The CPU budget

Every Go command, test binary, golangci-lint and vitest sizes itself to the
whole machine, and the gate runs up to six of them at once inside one slot
of eight: on 2026-10-26 the host ran at a load of ~150 on 32 cores, and
every phase, `tsc` included, was starved. The gate now takes the cores the
host has free, but never less than half the machine (16 on that host; a
floor of twice a slot's share, 8, starved lint's contract lane), as
`MG_CPUS`, and splits it: the test phase gets half (`go test -p`,
`GOMAXPROCS`) and a quarter for vitest's workers, the build, lint and a
generator running beside the others a quarter each. A generator that runs
first and alone (a dirty tree) gets all of it. `MG_CPUS=<n>` overrides the
budget.

### Direct importers only

A change to a hub package (`internal/logging`, `internal/observability`) reaches
about 177 test binaries through the full reverse-dependency closure. On a host at
load ~100 that is about 7 minutes of `go test -short`, so the merge gate's Go
fan-out stops after one hop (owner's decision, 2026-10-24). It runs the changed
packages, the packages whose files or tests import one of them directly, the
tree-walking guards, and every DB-backed package when a migration changed. Every
deeper dependent is listed in the plan as `deferred` and runs in the release
gate. `go build ./...` still compiles every package, so a deferred package can
only fail at test time, never at compile time, before the merge.

The web lane takes the same cut (`-reach direct`): a changed module runs the
vitest files that import it, and those importing a module that imports it (two
edges, as a Go package's own tests and its direct importers' tests are), and
lists the rest as `deferred`. A **hub**, a module more than 60 test files reach
within two edges (a store, the transport), runs only the tests that import or
`vi.mock` it by name: chatStore.ts reached 298 test files at two edges. And
when the change's modules together reach more than 150 test files at two
edges (a UI change touching many modules selected 130-330 in the merge queue,
at ~2s a file on the loaded host), every module runs only its by-name tests
(the plan's `web.edges` says which cut it took). A
**deleted** module no longer runs every vitest file (71 of the merge queue's 97
full web runs on 2026-10-25/26, ~27 minutes each): its importers had to change
too and select their own tests, and a module still importing it resolves
against the base tree's file list and selects the tests that reach it (`tsc -b`
fails on the dangling import first).

`task test:affected` keeps the full closure (`-reach all`,
the default), so a local run stays thorough. `MG_REACH=all task ci:merge` runs
the merge gate with it.

### What the release gate holds that the merge gate does not

Nothing the merge gate skips is dropped. Every check below still runs, on
main, in `task release:gate` (`scripts/release-gate.sh`): Docker-backed,
integration and e2e/Playwright tests (none of which the merge gate ever ran),
slow tests and the race detector (`task test`), golangci-lint and buf lint
(the unscoped `forge lint`), link errors (`go build ./...`), deferred Go
packages and vitest files (`task test` runs every one), and the whole
`forge generate` where the merge gate ran only the generate hooks.

### What the merge gate does not catch

These are the trade-offs. Each is caught by the release gate:

- **Tests of transitive dependents.** A behaviour change that breaks a package
  two or more imports away merges green. It is caught at release time, which
  makes it harder to bisect.

- **Slow tests.** A test marked `testkit.Slow` (or an older
  `testing.Short()` skip) never runs before a merge.
- **Vitest files further from the change.** Those more than two imports away,
  or more than one from a hub.
- **golangci-lint and buf lint**, over any package, and **link errors**.
- **The race detector.** Nothing before a merge runs `-race`.
- **Whole-project lint lanes.** golangci over packages the diff did not touch,
  the frontend lint, the graph, component-drift and config-reach lanes cannot
  be scoped, so `--scope` skips them.
- **Semantic interactions with a concurrent merge.** When main moves during the
  gate, the queue lands without re-running the gate if main's new commits touch
  none of the branch's files and none of the project-wide inputs: pins, proto,
  migrations, the Taskfile, the selector. In that case it only re-checks that
  the result builds. Two such changes can still interact through behaviour,
  with each one green alone.
- **Integration-tagged and e2e tests.**

## Slow tests: what moved to the release gate

A test that takes more than about two seconds under `-short` is a **slow test**.
It calls `testkit.Slow(t, "<what makes it slow>")` first
(`github.com/reliant-labs/forge/pkg/testkit`). The call skips the test under
`-short` and in the integration lane, so the test runs once, in `task test` /
the release gate. Gate a slow test; never weaken its assertion. If a test is slow
only because it waits out a production timeout, make it fast instead: inject the
duration through a package-level var or an option.

The first sweep (2026-10-24) found the slow tests by measurement. It ran
`go test -short -json -count=1 ./...` over the whole module at a host load of
about 100 on 32 cores. Every test that took 6s or more (about 2s on an idle
machine, given the roughly 3x contention) was then re-run on its own. The 325
tests that took 2s or more in both runs were gated. One test (cliauth, 14.5s in
the first run and 0.02s in the second) was a contention false positive and was
left alone.

| package | tests gated | what makes them slow |
|---|---|---|
| `internal/worktreereclaim` | 51 | real git repositories, git subprocesses |
| `internal/db` | 46 | many round trips to pgtest's Postgres |
| `internal/handlers/trigger` | 41 | DB-backed handler tests |
| `internal/workflow/runtime/activities/handlers` | 38 | activities against pgtest's Postgres |
| `internal/worktreesweep` | 33 | real git worktrees |
| `internal/handlers/worktree` | 26 | git through the handler |
| `internal/toolexec/daemonruntime` | 24 | real checkouts and worktrees on disk |
| `internal/provetest` | 24 | the fix's test runner and git as subprocesses |
| `internal/threads` | 18 | DB-backed thread trees |
| `internal/handlers/chat`, `gitutil`, `ghdelegated`, `pid1`, `llm/tools`, `handlers/inbox`, `launch`, `scenario/runner`, `catalog/github`, `daemon` | 24 | the same causes, a few tests each |

So a DB-backed or git-backed test that is fast still runs on every merge. One
that is slow runs before every release.

### The per-test budget

Nothing made a NEW slow test call `testkit.Slow`, so one merged silently and
taxed every later change reaching its package. The merge gate's Go step now
runs `go test -json` through a filter (`tools/test-affected/budget.go`) that
prints what plain `go test` would and times every top-level test against a
budget: 10s on an idle machine (five times the 2s line, so noise near it is
not judged), scaled 1x to 6x by the load per core, as vitest's timeout is. A
test **this change adds or edits** (the same list as the flake check) that
runs over it fails the gate, naming `testkit.Slow`. Any other test over it is
listed as `over-budget` and does not fail: the change did not write it, and
failing it would fail the wrong author. Gate those as you find them.
`MG_TEST_BUDGET=<duration>` (or `0`) overrides the budget;
`tools/test-affected run -test-budget auto` applies it outside the gate.

## The release gate

```sh
task release:gate                       # origin/main, in the background: 40-70 min on a loaded host
scripts/release-gate.sh run <sha>       # a specific commit of main
scripts/release-gate.sh check [<rev>]   # 0 green | 1 red | 3 no record
scripts/release-gate.sh latest          # newest green commit on main
```

The gate runs in a **private detached checkout** of exactly the commit it
judges (`~/.cache/reliant-release-gate/<clone>/snap`). That way your checkout
can be dirty, behind or on a branch, and main can keep merging during the 40-70
minute run, and neither changes what the gate judges. It holds one heavy slot
for the whole run. Every step runs even after a failure, and the run ends with
one table.

**The record.** Each run writes two kinds of record:

- `<git-common-dir>/release-gate/<sha>.json` and `.md`. These live outside
  every tree and are shared by every worktree of the clone.
- A git note on the commit under `refs/notes/release-gate`, pushed to origin.
  A release cut on another machine finds this one.

A record names its steps, their seconds, the forge pin and the host. A run of
only some steps (`RG_ONLY=lint,test`) is never recorded.

**The release cut.** A release has three steps:

1. `task release:rc` (or `:patch`, `:minor`, `:major`) opens the version-bump
   PR. Merge it.
2. `task release:gate` (or `scripts/release-gate.sh run <the bump's sha>`).
3. `task release:tag` tags the bump commit. It runs
   `scripts/release-gate.sh check` on that commit and refuses without a green
   record.

A green recorded on a first-parent ancestor (up to 50 commits back) also counts
when every change since it is release metadata: docs, `*.md`, or a change to
`electron/package.json` / its lockfile whose only difference is the `version`
field. So you can run the gate on main before opening the bump PR. As long as
nothing else merges in between, the bump commit inherits that green.

A hotfix that cannot wait takes `scripts/release-tag.sh --skip-release-gate`.
Say so in the release notes. There is no waiver for a red run: fix it.

## The merge queue

The fleet's merge queue (`.fm-tools/merge_queue3.sh`, outside this repo) is a
thin orchestrator over `scripts/merge-gate.sh`. Branches gate in parallel, one
per pool checkout. There are three pools. The host's heavy slots are the
limit, not the number of checkouts, so more pools would only add gates that
queue for a slot.

Pools are granted **first come, first served** (`.fm-tools/mq3/sched.sh`). A
branch takes a ticket when it arrives. It gets a pool once every earlier ticket
has one. While it waits it prints its place in line and an estimated wait, from
the median time recent runs held a pool. It prints this at the start and every
3 minutes. `merge_queue3.sh --status` prints every queue. A run that dies loses
its place, and the run behind it moves up at once. A rerun gets its old place
back when the last run failed for a reason that was not the branch's:

- a host flake (an OOM kill or a build-cache trim);
- a test timeout;
- main moving into its files until its gates ran out;
- any other gate failure, if you rerun it with `--flake-retry`.

A run keeps its old place for up to 6 hours and 3 reruns.
`.fm-tools/mq3/sched_test.sh` tests the scheduler.

On its pool checkout, each branch's run:

1. merges main in (never a rebase);
2. re-versions the branch's migrations above main's;
3. squashes onto main;
4. runs `scripts/merge-gate.sh --base <main> --amend` in one heavy slot.

Only the land step is serialized under one lock: fetch, push, post the
`merge-gate` commit status on the verified commit, and squash-merge. If main moved, the queue carries the verified commit onto the new
main and re-checks only that it builds, as long as main's new commits are
disjoint from the branch's files and from the project-wide inputs. Otherwise it
runs the gate again. The queue's summary, posted into each merge PR's body,
separates the time queued for a heavy slot from the time spent running.

### The required status, and the audited waiver

`main` accepts a merge only when the commit carries a SUCCESS `merge-gate`
commit status: the ruleset "main: merge-gate required" requires it, requires the
head to be up to date with `main`, and has **no bypass actors**, so `gh pr merge
--admin` is refused too. The queue posts that status on the exact squashed
commit its gate verified:

```sh
.fm-tools/merge_queue3.sh <remote-branch>
```

The only other way in is an audited waiver, for a hotfix that cannot wait or a
gate failure that reproduces identically on `main`. It posts
`WAIVED: <reason>` as the status, comments on the PR and appends to the
waiver log:

```sh
.fm-tools/merge_gate_waive.sh --by "<agent/thread or human>" --reason "<check + evidence>" <pr>
gh pr merge <pr> --squash
```

There is no waiver for the release gate: a red release-gate run is fixed, not
waived (`scripts/release-tag.sh --skip-release-gate` is the hotfix escape for
tagging, said in the release notes).

## CI

`.github/workflows/ci.yml` is a thin wrapper over these same commands: every
job is setup, then `task <name>`, so CI cannot disagree with a local run.

| Event | Job | Runs |
|---|---|---|
| `pull_request` | `Merge gate` | `task ci:merge -- --base <the PR's base>`, `task test:flake` (each new or changed Go test three times), `task ci:repo-checks` |
| push to `main`, nightly, `workflow_dispatch` | `Release gate` | `task release:gate -- <sha>`, then `task ci:repo-checks`. On `main` it publishes its record as the git note above, so a CI green counts for `task release:tag` |

Beside them run the lanes neither gate covers, selected by path
(`scripts/ci-lanes.sh`, `task ci:lanes`) on a pull request and all of them off
one (the contract only when a push changed what it checks): the renderer's release build (`task build:web:verify`), Playwright
(`task test:web:e2e`), the electron package (`task build:electron:smoke`) and
the vendored control-plane contract (`task contract:controlplane:check`, which
reads private control-plane with a deploy key). `CI result` is the one verdict.
DB-backed tests boot forge pgtest's embedded Postgres on the runner: there is
no Postgres service container and no `DATABASE_URL`.

