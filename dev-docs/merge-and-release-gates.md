# The merge gate and the release gate

**Review first (owner, 2026-10-30).** No CI minutes are spent on a head nobody
has reviewed. A pull request push runs nothing heavy; `pr-review` reads the
diff, not CI results; a passed PR enters GitHub's merge queue, where the merge
gate runs **once**, on main plus the entry, and decides the merge. The whole
suite runs on main after the merge and never blocks one: a red main keeps a
`ci:main-red` issue open until it is fixed, and `task release:tag` refuses a
commit without a green release-gate record, so it is fixed before the next
release. Details: [CI](#ci).

**Target: ≤5 minutes wall time; slow suites gate releases, not merges (owner,
2026-10-28).** The merge gate runs what the diff can affect. When the rules cannot
scope a change (`go.mod`, `package.json`, config, an unclaimed path) it runs the
whole Go `-short` lane (a forge pin move alone selects by the forge packages it
changes: `dev-docs/testing-affected.md`, "A forge pin move") and electron's tests (5m13s and 40s measured on a loaded
host), and for a full web fallback `tsc -b` over every web source plus the vitest
files the diff's own web modules reach (below); the whole vitest suite (1033
files, ~22 min loaded) is the one lane left to the release gate's `task test`,
noted in the summary. It also runs electron's tests when electron reads
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
| **merge** | `task ci:merge` (`scripts/merge-gate.sh`) | the affected tests (`-short`, cached, no `-race`, a per-test time budget) of the changed packages and their **direct** importers, plus every **transitive** dependent when there are at most 20 more (`MG_REACH_CAP`), and the vitest files within two imports of a changed module; a compile (no link) of every package whose build contains a change, and of **every test package** (`testbuild`); golangci-lint over the packages with a changed `.go` file, findings in the changed files, plus staticcheck's SA1019 over the packages that use what the diff deprecates (`scripts/golangci-changed.sh`, the merge queue's `golangci (changed)` job, outside the queue); forge lint's whole-project `--graph --closed-sets --proto-format` lanes on every diff (`projectlint`); the web `tsc -b` (incremental on state this checkout wrote; `--force` on a first run or copied state) when a web lane is selected or a web file changed; forge lint's own lanes over the changed directories (no golangci-lint, no buf lint), in the merge queue too; the forge demo bundle's build when the diff reaches its import graph; migration-safety when a migration or forge.yaml changed, and migration order against main's newest (in the queue, also against the entries queued ahead); electron's tests when electron reads a changed file; the whole lane when the rules cannot scope a change; and the forge-generate and docs-generate no-op checks only when one of their inputs changed (`internal/app` included) | before **every merge**. The merge queue runs it on the squash it is about to land |
| **release** | `task release:gate` (`scripts/release-gate.sh run`) | everything: unscoped `forge lint` (golangci over the whole module, buf, the whole-project lanes, the frontends), `task generate:check`, `task generate:docs:check`, `go build`, the web `tsc -b`, the forge demo bundle's size budget (`task demo:bundle-size`), `go vet ./...`, `task test` as three steps (Go `-race -count=1` without `-short`, so every slow test runs, then web and electron in full), `task test:integration`, `task test:e2e`, and the Go coverage floor over their union (`coverage`, below). CI runs them as parallel parts (below) | on a commit of **main**, before a release. `task release:tag` refuses a commit without a green record |

## The merge gate

```sh
task ci:merge                    # your branch, against its merge-base with origin/main
task ci:merge -- --base <rev>    # a stacked branch
task ci:merge -- --skip-lint     # leave vet and forge lint to the release gate
task ci:merge -- --skip-golangci # leave golangci to a golangci (changed) job beside the gate (ci.yml's ci:run)
task ci:merge -- --skip-vitest   # leave the selected vitest files to the merge queue's vitest shards
```

`--skip-lint` drops `go vet` and the scoped forge lint and changes nothing else:
the diff base stays the merge-base with origin/main, and golangci, buf lint and
the compat checks (`scripts/check-compat.sh`) run as they do in the merge tier.
golangci stays because the queue's `golangci (changed)` job ejects a PR on its
findings, where the release gate, which runs vet and forge lint, ejects nothing.
control-plane's pr-fix gates a PR branch with it (control-plane#2189).
`--queue` (the merge queue's tier, below) drops `go vet`, and golangci, which
the queue's `golangci (changed)` job runs beside it, and keeps the scoped forge lint, plus
queue-entry semantics, which are wrong on a PR branch: `CI_QUEUE_ENTRY=1` makes
`check-compat.sh` require a single-parent HEAD and judge only `HEAD^..HEAD`, so
a branch with several commits or a merge of main would fail, or pass, on the
wrong diff. Never pass `--queue` outside the queue. Until 2026-11-13 `--queue`
implied `--skip-lint`, so the queue ran no forge lint at all, and #2696 (an
unguarded optional dep) and #2662 (three log calls without their context) turned
main red on findings the scoped lint names in about two minutes
(`/home/workspace/designs/reverse-walk-backtest-2026-11-13.md`, "fast lints").

**go test's own vet** (`GO_TEST_VET`, `Taskfile.yml`). `go test` runs a vet
subset over each package it tests and, for printf facts, over every
dependency, under action IDs no `go vet` run shares: cold, that is ~70s of the
~90s `go test` spends before running `./cmd/reliant/cmd`'s tests. A gate whose
own vet covers the packages it tests turns it off for its test step: the merge
gate's default tier (its vet phase vets `go.build` + `go.packages`, test files
included, and gate-verify fails the gate when that phase is missing) and the
release gate's `test-go` (beside `vet.sh ./...`; a hotfix gate's merge step).
Everywhere else it stays on: `--queue` and `--skip-lint` leave vet to the
release gate, so go test's vet is the only one they run, as it is for the SDK
module's step, `test:integration`, `test:e2e`, the flake gate and every plain
`task test` / `task test:short`. `-vet` is not in Go's test-result cache key,
so the gates still replay your `task test:short` results. An exported
`GO_TEST_VET` (empty included) wins over a gate's default; `task test
GO_TEST_VET=-vet=off` sets it for one run. There is no `-ldflags=-w` lane flag:
`go test` already links every test binary it runs with `-s -w`; only `-c`, `-o`
and the profile flags keep DWARF, and `task build:packages`' one `-c` link
passes `-w` itself.

It holds **one** host heavy slot for the whole run (`forge machine slot run`), and every
phase nests inside that slot, within a CPU budget (below). It prints one row per phase:

```
### merge-gate: PASSED          (#1427's own merge, load ~50 on 32 CPUs)

| phase | result | seconds | started +s |
|---|---|---|---|
| heavy slot (queue) | - | 86 | - |
| plan | PASS | 28 | 1 |
| pnpm-tools-protoc-gen-es | PASS | 10 | 32 |
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
   **pgtest** follows it when the whole forge generate or any Go test is
   selected: `go run ./tools/pgtest-check` asks forge pgtest for a scratch
   database on the shared test Postgres, the server every DB-backed test and
   forge generate's schema read use. It takes seconds. When the server cannot
   be reached or booted, the gate fails there, naming the shared server, and
   starts no other phase; before it, those phases failed minutes in, every
   test "connection refused" (control-plane#2189). `MG_PGTEST_TIMEOUT`
   (default 150s) bounds it.
2. **generate** runs only if the plan's forge-generate lane is selected (a forge
   input or output changed, `tools/test-affected/rules.go`'s `forgeInputs`; the
   composition root `internal/app/` is one since control-plane#3571, where #3499
   changed only its wiring and main's check failed on `gen/forge_service_catalog.json`).
   It runs
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
4. **build**, **testbuild**, **test**, **typecheck**, **golangci**, **lint**,
   **projectlint** and **migsafety** then run at the same time (after the generators, when they may rewrite the
   tree; below):
   - **build** compiles, without linking, every package whose build contains a
     changed package (the plan's `go.build`, at any distance), or `./...` when
     the Go lane is full: `go list -export`, which compiles each package for
     its export data and fails naming any that does not compile, main
     packages included. `go build ./...` also LINKED every main package (the
     server, the CLI, a dozen generators): 41s with every package already
     compiled, against 6s for `go list -export ./...` (2026-10-26). Link
     errors are the release gate's `go build`.
   - **testbuild** is `scripts/test-build.sh`: `go list -test -export` over
     every package with test files, compiled (never linked, never run), and
     each package whose tests sit behind one custom build tag (`e2e/stories`'
     `e2e`, `replaytest`'s `replayfixtures`, ...) under that tag. It runs on
     every diff in every tier, after build (whose compiles it reuses), and
     skips the packages the test phase runs (`--exclude`): `go test` compiles
     those, and side by side the two compiled the same packages twice. A required field or signature change that breaks a fixture
     in a test package no lane selected fails here, naming the file and line:
     #3489 kept main red for 30 hours with exactly that, refactor after
     refactor breaking fixtures more than one import away. Measured on 48 CPUs
     at load 30-45, after build: 11s with nothing to recompile (~80
     CPU-seconds, mostly loading the package graph), 11s after an
     `internal/db` change (the test phase runs every package it reaches), 42s
     (~620 CPU-seconds) after a change to `internal/workflowdrafts`, whose 152
     deferred test packages it compiles. main's `go-cache` job compiles the
     test packages too (`task build:packages`), so the queue's restored cache
     holds them. A fixture that compiles and fails at run time is the release
     gate's, with one exception the test phase catches from source: a
     construction that leaves a Deps field unset (below, "Deps fields").
   - **test** is `tools/test-affected run -plan <plan.json> -lanes go,web,electron
     -test-budget auto`. It runs the plan the plan phase wrote (re-planning
     loaded the whole package graph again: 17-141s), so the changed packages
     and the packages that import one by name, plus the rest of the
     reverse-dependency closure when it adds at most `MG_REACH_CAP` (20)
     packages, and the vitest files within two imports of a changed module
     ([below](#direct-importers-and-a-transitive-budget)). A Go test the change adds or edits that runs past
     the per-test budget fails it (below). On a **FULL web fallback** (a
     `package.json`, lockfile or web build-config change, which no rule can
     scope) it runs `-web-full reached`: the vitest files the diff's own web
     modules reach at the same two-edge cut, not none and not the whole suite
     (`dev-docs/testing-affected.md`, "A FULL web lane in the merge gate"). When `--amend` folded a
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
   - **golangci** is `scripts/golangci-changed.sh --base <the gate's base>`
     (`task ci:golangci`), the very command the merge queue's `golangci
     (changed)` job runs, so the two judge the same packages with the same
     config and release and cannot disagree ("golangci-lint before the queue",
     below). Only when the diff adds or changes a `.go` file, and not under
     `--queue` or `--skip-golangci`.
   - **lint** is forge lint's own lanes, named (`--contract --conventions
     --generated-drift --go-markers --proto-markers --create-nullability
     --computed-fields --read-only-fields --db-grants --column-markers
     --closed-sets --tracing --guarded-fields --optional-deps-guard`), with
     `--scope <every changed directory>`, a pin move included, in every tier
     but `--skip-lint` (the merge queue's included). `--optional-deps-guard`
     joined with the queue lint: `forge.yaml` makes the rule an error, and it
     was the one lane #2696's finding needed. golangci-lint, buf lint, the
     whole-project lanes and the deploy lanes run in the release gate's
     unscoped `forge lint`: on one loaded host the same two-directory scope
     took 331s with every default lane and 41s with these (golangci-lint is
     the bulk), and a pin move made the merge queue's lint unscoped, up to
     2735s. A pin bump's regenerated files are in its diff, so they are in
     scope; what a new version breaks elsewhere in the module is the release
     gate's to find.
   - **demo** builds the forge demo bundle, `WEB_BUILD_TARGET=demo vite
     build` in `frontends/web/` (into the log directory, removed after), when
     the plan's `web_demo` lane is selected: a changed file in the bundle's
     import graph, its vite config, plugins or `demo.html`, or a FULL web
     lane. Its `vite-plugin-demo-bundle.ts` guard fails the build on a
     Reliant-only module in the bundle, which `tsc -b` cannot see: #2669 made
     `useOrgs.ts` import `authStore.ts`, typechecked clean, and failed the
     v1.17.0-rc.3 cut. 1.8-2.0 minutes on a loaded host.
   - **webbuild** builds the renderer as a release does,
     `scripts/web-release-build.sh` (`NODE_ENV=production vite build` into a
     temporary directory, then `.github/scripts/verify-bundle.mjs` on it: the
     release gate's `task build:web:verify` without its eslint and the `tsc -b`
     the typecheck phase runs), when the plan's `web_build` lane is selected:
     a FULL web lane (`frontends/web/package.json`, `pnpm-lock.yaml`, the
     tsconfig, a root Node manifest, `sdk/ts`, ...) or the build's own inputs
     (`vite.config.ts`, `vite-plugin-*.ts`, `index.html`). vitest and `tsc -b`
     never bundle: #2829's supabase-js bump imported `node:module`, passed
     both, and failed the release gate (reliant#2850). A web source change
     alone does not select it; the release gate builds those. 4.2 minutes
     measured at load average 100 on 31 CPUs (2026-11-18); it runs beside the
     test and typecheck phases.
   - **migsafety** is `forge lint --migration-safety`, and runs only when a
     migration changed.
   - **projectlint** is `forge lint --graph --closed-sets --proto-format`,
     unscoped, on every diff in every tier: the whole-project forge lanes that
     take seconds (12-24s together at load 270 on 48 CPUs; `--closed-sets` 2s,
     `--proto-format` 1s alone). The scoped lint skips a lane it cannot scope,
     and each of these fails on files the diff need not touch: `--graph`, a
     provider no root reaches (#3483: `internal/planning`); `--closed-sets`, a
     CHECK declared `forge:closed-set` (access_tokens' scopes) that no longer
     lists exactly the set the pinned forge carries, which a pin bump grows
     (#3382; the fix is a widening migration,
     `task db:migration NAME="<name> --closed-set <constraint>"`);
     `--proto-format`, a `.proto` buf format would rewrite (#3397, #3480). It
     replaced the closedsets phase, which ran only on a migration, `go.mod` or
     `forge.yaml` change. The other whole-project lanes (golangci over the
     module, the frontends, component-drift, config-reach) stay in the release
     gate: they take minutes.
   - **migorder** is `go run ./tools/test-affected migration-order`, and runs
     only when a migration changed. It fetches `origin/main` and fails a
     migration versioned at or below main's newest, which golang-migrate would
     never apply (reliant#1399). It fetches because migsafety reads the local
     `origin/main` as last fetched, and a branch reviewed while main moved
     passes it against a stale head. Under `--queue` it adds `-also HEAD^`:
     the merge group's HEAD is this entry's squash on top of main and the
     entries queued ahead, none of them on `origin/main` yet, so it also fails
     a migration the entry adds at or below the newest one the entries ahead
     carry. #3482 and #3483 were queued together, each above main's newest,
     and #3483's `20261124202455` landed below #3482's `20261124204326`
     (control-plane#3571).

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
compares what the plan **obliges** (build, test, testbuild, projectlint, plus
`generate`, `docs`, `migorder`, `typecheck`, `demo`, `migsafety`, `lint`,
`golangci` as the plan, the diff and the tier select them) and what the gate **started** against the rows. A phase with no row
(queued for a slot and cut off, cancelled, timed out, its tool missing) fails
the gate and is named: `FAILED: docs never completed (no result row; ...)`.
The verdict is also written to `$MG_LOGDIR/verdict`. A missing `jq` or an
unparsable `plan.json` aborts the gate, because every lane would otherwise read
as "not selected". reliant#1393 merged with docs-generate still queued; this is
the check that stops that. CI's `Merge gate` job, and so `CI result`, fails
unless the verdict is `OK`.

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

Memory is budgeted too. `forge generate` peaks at ~7.5 GB RSS on its own, and
beside `tsc -b` (~2.3 GB) and the test links (~2.7 GB each) the gate reached
13 GB in its first five minutes on reliant#1772. The merge queue's hosted
`ubuntu-latest` runner has 16 GiB: it ran out, its agent died ("The runner has
received a shutdown signal") and the job sat until its timeout, three times.
So on a host under `MG_GEN_ALONE_GIB` (32) GiB, the smaller of physical memory
and the cgroup's `memory.max`, `forge generate` runs first with the whole CPU
budget, and the other phases start when it is done. Only the `prewarm` phase
runs beside it: it compiles (never links) the selected test packages, niced and
at a quarter of the budget (each compile well under 1 GiB), so the test phase
after generate only links and runs. In the merge queue generate averaged 313s
and test 281s, one after the other, on 8 of 16 runs (2026-11-05/06).
`MG_MEM_GIB=<n>` overrides the detected size.

The two vite builds are deferred on the same small host. The forge demo bundle
(`demo`) peaks at ~9 GB RSS and the renderer's release bundle (`webbuild`) at
~12 GB, each on its own (reliant#2935 merged with main, 2026-11-21). Beside
test, `tsc -b` and lint, the demo build alone took the queue's gate to
14.6 GiB of the runner's 16 GiB; with the release build too, the runner lost
its agent on four queue entries in a row (#2935 and #3009, two each), which
the queue reports as `CI result` cancelled at its 30-minute timeout, often
with no log. So under `MG_GEN_ALONE_GIB` they run after every other phase has
finished, one at a time (`run_bundles`): slower in wall time, but never
beside anything. A bigger host still runs them beside the rest.

**Rollout check for the prewarm (reliant#2308).** It was estimated, not
measured, so after it merges read the first 5 merge-queue runs (`merge-queue.yml`)
whose summary carries the note `forge generate ran before the other phases,
test packages compiled beside it`, and record for each, in the PR: the job's
conclusion, the `memory peak` note (the gate's cgroup `memory.peak`, where the
runner exposes it), and the `generate`, `prewarm` and `test` phase seconds and
the wall time. It fails if any of them:

- loses its runner ("The runner has received a shutdown signal", or the job
  ends at its timeout without a summary);
- peaks above 14 GiB of the runner's 16 GiB;
- has a `test` phase no faster than the 281s baseline above, or a `generate`
  phase more than 20% slower than its 313s.

On a failure, revert the prewarm launch (the `phase prewarm prewarm_tests &`
line and its `wait` in `scripts/merge-gate.sh`): generate then runs alone and
serialized again, as before reliant#2308.

### Direct importers, and a transitive budget

A change to a hub package (`internal/logging`, `internal/observability`) reaches
about 177 test binaries through the full reverse-dependency closure. On a host at
load ~100 that is about 7 minutes of `go test -short`, so the merge gate's Go
fan-out stops after one hop (owner's decision, 2026-10-24). It runs the changed
packages, the packages whose files or tests import one of them directly, the
tree-walking guards, and every DB-backed package when a migration changed.

The rest of the closure runs too when it is small: when it adds at most
`MG_REACH_CAP` packages (default 20; `tools/test-affected plan -go-reach-cap`,
the plan's `go.reach_cap`), and the plan then says `reach all`. Above it, every
deeper dependent is listed in the plan as `deferred` and runs in the release
gate. Measured on 48 CPUs at load 45-95, `-p 24` (2026-11-25), a synthetic
export added to each package:

| changed package | direct | whole closure | the added packages' `-short` run | at the default 20 |
|---|---|---|---|---|
| `internal/handlers/chat` | 16 | 18 | +2: 42s | selected |
| `internal/db` | 119 | 165 | +46: 46s | deferred |
| `internal/workflowdrafts` (imported by `internal/llm/tools`) | 15 | 166 | +151: 193s | deferred |

The default is set by the merge queue's runner, not this host: 4 CPUs, the
test phase at half of them, so 46 more test binaries (internal/db's) would add
minutes to every internal/db change. 20 bounds it to some 20 links and runs.

`testbuild` compiles every test package on every diff, so a deferred package
can still only fail at test time, never at compile time, before the merge.
`MG_REACH_CAP=0` restores direct importers only.

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

### The Go coverage floor (owner, 2026-11-26)

The release gate's `coverage` step holds total Go statement coverage to the floor
in `coverage-floor.txt`, which only goes up. There is no push to write tests: the
floor stops coverage from sliding. It **gates releases only**, never the merge
gate. The step unions every Go cover profile of the run: the unit lane of each go
shard, plus integration and e2e. A block counts as covered when any `go test`
invocation hit it. Packages with no test files count at 0%, because the go shards
split them too. Generated code (`gen/`, `testdata/`, `*_gen.go`, `*.pb.go`) is left
out, as forge's coverage report leaves it out (`scripts/coverage-floor.sh`, the
same file as control-plane's). In CI each part leaves its unioned profile beside
its part record (`<part record>.cover.out`), and `assemble` judges the union. A go
part with no profile fails the step: an unmeasured shard cannot pass. The record
carries the number (`.coverage`). To raise the floor, run
`task coverage:raise-floor`. It reads the newest record's number
(`release-gate.sh coverage`), subtracts 0.5 points of headroom for noise, and
writes the result only if it is higher than the current floor. To lower the
floor, run `scripts/coverage-floor.sh raise --to <percent> --lower --reason
"<why>"`, which records the reason in the file. Give the same reason in the
commit.

### The demo bundle's size budget (owner, 2026-11-24)

The release gate's `demo-size` step (`task demo:bundle-size`,
`scripts/demo-bundle-size.sh`) builds the forge demo as forge ships it and
fails when its initial or total gzip is over
`frontends/web/scripts/demo/bundle-budget.json`. It **gates releases only**: the
merge gate builds the demo for its guard (`vite-plugin-demo-bundle.ts`), never
for its size, and a pull request that moves the demo pastes
`task demo:bundle-size -- --report-only` (with `--before`, its before and after)
into its body instead. In CI it runs in the `webbuild` part, after the
renderer's release build; the RC train runs it in `static`. What it measures,
today's numbers and how a ceiling moves: `dev-docs/demo-inventory.md`, "Bundle
size".

### What the merge gate does not catch

These are the trade-offs. Each is caught by the release gate:

- **Tests of transitive dependents of a hub.** When a change reaches more than
  `MG_REACH_CAP` packages beyond its direct importers, a behaviour change that
  breaks a package two or more imports away merges green (a fixture that stops
  compiling does not: `testbuild`). It is caught at release time, which makes it
  harder to bisect.

- **Slow tests.** A test marked `testkit.Slow` (or an older
  `testing.Short()` skip) never runs before a merge.

  **Deps fields** are the exception. A pull request that adds a field to a
  service's `Deps` and wires only the tests the merge gate runs leaves the slow
  and build-tagged constructions without it, and main goes red with a nil
  panic (#3368 → #3589, #3599 → #3605, #3448/#3507). `internal/architecture`'s
  `TestEveryConstructionSetsEveryRequiredDepsField` reads every Go file,
  whatever its build tags, and fails a `Deps` literal that leaves out a field
  not marked `// forge:optional-dep`: every one outside the declaring package,
  and every one passed to `New` inside it. It runs on every Go change (a
  walker). It reports only additional missing fields relative to the queue
  base (`HEAD^1` on `merge_group`, otherwise the merge base with origin/main;
  `DEPS_GUARD_BASE` overrides it). Baseline findings from unrelated queued PRs
  cannot fail it. Counts are compared per file, Deps type and field, ignoring
  line shifts. Adding a required field still checks unchanged constructions
  everywhere; wire it everywhere or mark it optional. It cannot
  see a construction that builds `Deps` field by field, nor tell which methods
  need a field: `threads.Deps.Chats` (needed only by the interrupt and pause
  paths) is exempt in `internal/threads`, whose tests mostly build a
  creator-only service.
- **Vitest files further from the change.** Those more than two imports away,
  or more than one from a hub. A FULL web fallback runs the same reached set,
  so a dependency bump that breaks a test no changed module reaches merges
  green.
- **golangci-lint and buf lint**, and **link errors**. The merge gate lints
  the changed packages with golangci-lint ("golangci-lint before the queue",
  below), and in the queue a job beside it does, so what reaches main unlinted
  is a finding in a file the diff did not change, or behind a build tag. buf
  lint runs only for a proto change.
- **The race detector.** Nothing before a merge runs `-race`.
- **The slow whole-project lint lanes.** golangci over packages the diff did
  not touch (except SA1019 where the diff deprecates), the frontend lint,
  component-drift and config-reach cannot be scoped and take minutes. The fast
  ones (graph, closed-sets, proto-format) run in `projectlint`.
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

A hotfix is cut from the commit prod runs and gated narrowly (below,
"Hotfixes"). One that cannot wait even for that takes
`scripts/release-tag.sh --skip-release-gate`. Say so in the release notes.
There is no waiver for a red run: fix it.

### Release candidates: gate a frozen commit, never main's tip (owner, 2026-10-31)

Main keeps merging all day and is never frozen, paused or blocked for a
release. A release ships a **release candidate**: one commit of main, cut
off as a branch, gated, and promoted unchanged. A captain spent 28 hours
re-gating main's moving tip and shipped nothing; this is the rule that stops
that.

1. **Cut.** Push the commit as a branch, `release/vX.Y.Z-rc.N`, and mark its
   head with the tag `rc/vX.Y.Z-rc.N`. Choose a commit whose
   `electron/package.json` already carries `X.Y.Z` (the bump PR merged), so it
   can be tagged without a new commit.
2. **Gate that SHA only.** `scripts/release-gate.sh run <rc sha>`. CI's
   push-to-main run of the same sha is an equal record. Nothing that merges
   afterwards is in this RC: it goes in the next one.
3. **Red:** file the failures for main's fixers (`pipeline:main-red`). Fixes go
   to **main**, never to the RC branch. Cut `rc.N+1` from a newer main that
   carries them. More than one RC may be in flight; the first green one ships.
4. **Green:** `scripts/release-tag.sh vX.Y.Z --commit <rc sha>`. The release
   tag lands on exactly the commit the gate judged, so what ships is what was
   gated.

The RC train (control-plane `scripts/rc/rc.sh`) gates the set's reliant commit
on its own machine (owner, 2026-11-02): `scripts/rc/rl-gate.sh` runs this
gate's parts (`release-gate.sh run --part`) and ci.yml's Playwright and
electron lanes in parallel, assembles the record and publishes it as the
commit's note. `release-tag.sh --rc` accepts that green record, and otherwise
the commit's `Release gate` job on Actions (the train's fallback, ci.yml
dispatched at the RC branch, which publishes no note off main).

The RC tag is `rc/…`, **not** `vX.Y.Z-rc.N`. A `v*.*.*` tag push runs
`Release Electron App` (which now refuses a prerelease tag: the alpha
channel is retired) and `Build & Push Image`, for a commit nobody has
gated yet. Two `v*` tags on one commit would also make
`scripts/release-identity.sh`'s `describe --exact-match` ambiguous. The
`release/` branch and the `rc/` tag trigger no workflow.

### Hotfixes: prod's commit plus the fix, gated narrowly (owner, 2026-11-20)

A `release:asap` fix whose train set is red, or that is still not in prod two
hours after it merged, ships on its own: control-plane's rc-train **hotfix
lane** (control-plane `docs/pr-pipeline.md`, "Hotfix lane") cuts it from the
reliant commit prod runs, never from main.

1. **Cut.** The commit prod runs, R', then each fix PR's squash commit
   cherry-picked onto it (`RC-Set:` trailer, so the head keeps the RC shape),
   then the version bump `scripts/hotfix-bump.sh vX.Y.Z` writes: the version
   after prod's (above every final tag the commit contains), its notes
   (`tools/releasenotes`) and the regenerated changelog. It is pushed as
   `release/vX.Y.Z-rc.N`, like any RC. A pick that conflicts, or that changes a
   migration or the forge pin, is refused: that fix waits for the train.
2. **Gate the picks, on top of prod's green.** `scripts/release-gate.sh hotfix
   --base <R'> <head>` runs the merge gate against R' with every dependent of a
   change (`MG_REACH=all`) and the Go tests of each changed package under
   `-race` with no `-short`, and records `{kind: "hotfix", base}`. `check`
   passes that record only while R' is its ancestor and is itself green: the
   whole release gate judged prod's commit, and the hotfix gate judges what the
   picks can reach.
3. **Tag.** `scripts/release-tag.sh vX.Y.Z --commit <head> --rc --hotfix`: the
   version is the RC head's own, not main's (main declares the next release),
   and the head must be on a `release/` branch.

When main's bump PR later declares the same version, that version is burned and
the train asks for the next bump, as after any release.

## The merge queue

`main` merges only through GitHub's native merge queue. The ruleset
"main: CI result + merge queue" requires the `CI result` check, squash-merges,
and lists one bypass actor, the `reliant-01` account the pipeline acts as
(never use it to bypass: no `--admin`). `merge-queue.yml` runs on
`merge_group`, so each queued PR is tested on main plus the entries ahead of it,
and lands only if that run is green. The work is ONE job, `Merge gate`, running
`task ci:merge -- --queue --skip-vitest`; the required `CI result` is a
few-second job that `needs:` it, `golangci (changed)` and `vitest` (below) and
passes only when each succeeded. The gate's budget is five minutes of wall time, runner
queue included (owner, 2026-11-01): the queue tier leaves `go vet` to the
release gate (and golangci to the job beside it) but runs the scoped forge
lint, plus the queue entry's semantics: buf lint only for a proto change, and
compat judged on the entry's own squash. The renderer's
release build, Playwright and the electron smoke run on main, where the release
record requires them. Its last step reports the run's wall time split into
runner wait and run time, and files one rolling `pipeline:slo` issue when it is
over budget (it does not fail the group: a slow runner is not the change's
fault). Beside it runs one more job, `golangci (changed)`, which `CI result`
needs and requires (next section). That `merge_group` run is the only run of the merge gate an
unlabelled PR gets. On the PR itself `CI result` passes within a minute (review
first), which is what lets GitHub enqueue it: GitHub enqueues only a PR whose
required checks are green on its head. Only the `pr-review` workflow enqueues,
with `gh pr merge <pr> --squash --auto --match-head-commit <sha>` when a review
passes (control-plane `docs/pr-pipeline.md`); an author opens the PR non-draft,
adds no queue label (the pass adds `merge:queue`) and never runs `gh pr merge`
(`--auto` included). In the queue,
the required `Reliant review` check (`reliant-review.yml`) requires
`pr-review`'s pass marker on every PR head in the group
(`scripts/ci-review-marker.sh`, fetched from main). Because the PR's own `CI result` is
green within a minute, auto-merge an author arms before the review enqueues
the unreviewed head at once: its group fails `Reliant review`
(`latest: none`), and so does every group behind it. A queue failure dequeues
the PR, which the pipeline's fixer picks up.

Run `task ci:merge` locally before asking for a merge: it is the same gate CI's
`Merge gate` job runs, and a red queue run costs every PR behind yours a retry
(the queue builds up to five entries at once; a failure rebuilds those behind
it).

### golangci-lint in the queue (owner, 2026-11-08)

On 2026-11-08 main went red three times on golangci findings that nothing
before the merge ran: staticcheck QF1001 from #2293 and from #2427's test, and
gosec G101 from #2368. Each was caught only by the release gate's unscoped
`forge lint`, after the merge. So the queue lints again, without growing the
gate's five minutes:

- **What.** `task ci:golangci -- --base <the group's base>`
  (`scripts/golangci-changed.sh`): `golangci-lint run --new-from-rev=<base>
  --whole-files` with the root `.golangci.yml`, over every package of this
  module with a changed `.go` file (by `scripts/lint-scope.sh`'s rule, so not
  the root or `sdk/go`; testdata and build-tag-only directories are dropped
  too). It reports the findings in the files the diff changed, not only on its
  lines: a linter that reports a block on its first line (forge #1930's
  nestif, grown by lines inside an `if` it never touched) is invisible line by
  line. Main is lint-clean (the release gate holds it there; the whole module
  had 0 findings at 87450cd24), so a changed file carries no backlog to trip
  over. Run it locally with `task ci:golangci` (base: the
  merge base with `origin/main`); `-- --plan` lists the packages.
- **Which golangci-lint.** The pinned forge's `templates.GolangciLintVersion`,
  the release the release gate's `forge lint` requires, read from the forge
  module's source by the setup action (`golangci: "true"`) and installed from
  its GitHub release, cached by version (saved by `go-cache` on main), with the
  download's retries and API fallback.
- **Parallel, and gating.** It is its own job, `golangci (changed)`, that
  starts with `Merge gate`, not after it: neither needs the other. `CI result`
  `needs:` exactly those two, runs `if: always()`, and passes only when both
  `needs.<job>.result` are `success`: the ruleset requires one check, and a
  job it does not require stops nothing. It uses no checkout, token or API.
  (Until reliant #3095 it polled the run's jobs API anonymously for the lint's
  verdict; that API answered 403 and turned groups whose gate and lint had
  both passed red after eight polls.) A group with no changed `.go` file
  skips the job's steps, not the job, so it is a success in seconds, before
  any toolchain is installed. A `ci:run` pull request runs the same job in
  `ci.yml`, and its `CI result` requires it too.
- **What it costs.** Measured on GitHub's runners (2026-11-08) over the four
  diffs of that day, each reproducing its finding: 96-179s of job wall time
  (checkout ~13s, setup 63-107s, almost all of it restoring the Go module and
  build caches, then the lint itself 9s for one file, 13s for one package,
  60s for four, 61s for #2368's thirteen). The gate runs 230-390s on the
  same runners, so the lint is done well before it. `CI result` then costs
  one more runner start after both finish.
- **Checking it after the merge.** Each merge group's job list shows
  `golangci (changed)`'s wall time beside `Merge gate`'s, and `CI result`
  starting once both have finished.
  The first push to main then runs the setup action's "Save golangci-lint
  (main only)" step in `go-cache`, and the next merge group's "Restore
  golangci-lint" reports a cache hit and downloads nothing.
- **Rollback.** If the job costs the queue more than its budget, or fails for
  a reason that is not a finding (a download, the cache), revert the PR that
  added it: a plain `git revert` of one squash commit, with no data, schema or
  ruleset change to undo (the ruleset requires `CI result` alone, which then
  stops needing the job). Its cached binary goes unread and GitHub evicts
  it after 7 days.
- **What it still leaves to the release gate.** Packages the group did not
  change, findings in files it did not change, the build-tagged pass, and buf
  lint.

### The merge queue's vitest files, in four shards (owner, 2026-11-21)

A hub web module reaches a thousand vitest files at the gate's two-edge cut:
#2935 (`paths.ts`, `orgInterceptor.ts`, `configReady.ts` and the web_config
proto) selected ~1096 beside 95 Go packages, and its group was cancelled at the
gate's 30 minutes twice; #3009 the same. One 4-CPU runner ran them in one
process beside the Go tests. Capping the web reach would drop coverage, so
the queue splits the same list instead.

- **What runs where.** The gate runs `--skip-vitest`: its test phase keeps
  the Go and electron lanes, and `tsc -b` stays. The `vitest` job, a matrix of
  four legs (`fail-fast: false`, 30 minutes each), runs
  `scripts/vitest-shard.sh --base <the group's base> --shard <i>/4`. Each leg
  plans the group exactly as the gate does (`tools/test-affected plan -reach
  "${MG_REACH:-direct}"`, the same base and rules; a FULL fallback runs the
  reached files) and runs `test-affected run -lanes web -web-full reached
  -web-shard <i>/4`, which is `vitest run --shard=<i>/4 --passWithNoTests
  <the plan's files>`. vitest splits one sorted list, so the four legs run
  every selected file once and none twice.
- **Never skipped.** A group whose plan selects no vitest file passes each
  leg once the planner has run (Go setup without the build cache, about a
  minute), with no install, so `needs.vitest.result` is always `success` or
  `failure`. A leg whose plan fails or cannot be read fails.
- **The verdict.** `CI result` needs `vitest`; `needs.vitest.result` is the
  matrix's roll-up, `success` only when every leg succeeded, and the
  aggregate requires it like the other two. A `--skip-vitest` gate with no
  shard job beside it, a dropped leg or a shard count out of step fails
  `tools/test-affected` (`TestMergeQueueVitestShards`,
  `TestSkipVitestOnlyBesideShards`).
- **Locally.** `task ci:merge` is unchanged: every lane, vitest included, in
  one process. `scripts/vitest-shard.sh --base <rev> --shard 1/4` runs one leg.
- **What it costs.** Four more runner starts per group, each from the start
  of the group (no `needs`), so they add no hop; the org's 20-job limit is
  the trade.

### golangci-lint before the queue (owner, 2026-11-18)

The queue's job made golangci gate the merge, but nothing ran it before the
queue: on 2026-11-18 it ejected #2857 and #2873 on findings the merge gate
had passed, costing each a review round and every entry behind them a rebuilt
group (Reliant bug 67d80734). So `task ci:merge` runs the same lint as its
**golangci** phase:

- **The same check.** The phase runs `scripts/golangci-changed.sh --base <the
  gate's base>`, the script and base the queue's job runs (`task ci:golangci --
  --base <the group's base>`): the same package selection, root
  `.golangci.yml`, `--new-from-rev --whole-files` and release. The script
  reads the release from the pinned forge's `templates.GolangciLintVersion`, as
  the setup action does, and fails (exit 2, naming the `go install` command)
  when there is no `golangci-lint` on PATH or it reports another release,
  because another release reports other findings; the gate's summary prints
  that message as the failed phase's last lines (`GOLANGCI_LINT_VERSION` overrides the lookup). Its
  diff is the working tree against the base, untracked `.go` files included,
  which is what `--new-from-rev` reads; on a clean checkout, as in the queue,
  that is exactly `<base>..HEAD`.
- **When.** Only when the diff adds or changes a `.go` file (the job's own
  "Changed Go files" rule, plus untracked files); otherwise the summary notes
  `golangci: not run`. It runs in `task ci:merge` and under `--skip-lint`,
  which control-plane's pr-fix uses, so a fixed PR does not go back into the
  queue with a finding. It does not run where a `golangci (changed)` job runs
  beside the gate on its own runner: under `--queue`, and under
  `--skip-golangci`, which `ci.yml`'s merge gate on a `ci:run` pull request
  passes. That job installs golangci-lint and the gate's runner does not, and
  the job lints once instead of twice. The gate then writes
  `golangci-elsewhere` to its log directory. `gate-verify` obliges the phase on
  a `.go` change unless that marker is there, so a golangci phase that never
  completed fails the gate by name. `TestEveryMergeGateCallerProvisionsGolangci`
  holds every workflow step that runs the gate to either installing the pinned
  golangci-lint (the setup action's `golangci: "true"`) or delegating to such
  a job under the same condition.
- **Deprecations.** A declaration the diff marks `Deprecated:` (by hand, or in
  the code a proto's `deprecated = true` generates) turns every use of it into
  staticcheck's SA1019, in files the diff did not change, which the pass above
  never reports. The script finds each such declaration in the diff, then runs
  `golangci-lint --enable-only staticcheck` over the packages that declare or
  import it and name it, and fails on an SA1019 finding there. #2930
  deprecated `ListMessagesResponse.Total`; the unchanged
  `internal/handlers/chat/rpc_list_messages.go` still wrote it, and only the
  release gate's whole-module lint said so (#3005). A diff that deprecates
  nothing costs only the scan; one deprecation in `internal/db` used by one
  other package added 5s warm.
- **Slot and lock.** It runs beside build, test and lint inside the gate's one
  heavy slot, at a quarter of the gate's CPU budget (`GOMAXPROCS`), like lint.
  golangci-lint takes a machine-wide lock and, given no flag, exits 3 after
  five seconds ("parallel golangci-lint is running") whenever anything else on
  the machine is linting: in the gate's own slot it drops the lock
  (`--in-slot`, `--allow-parallel-runners`), as forge lint does in a slot of
  its own; anywhere else, the queue's job included, it waits for it
  (`--allow-serial-runners`). Neither changes a finding.
- **What it costs.** `golangci` has a 120s step budget in
  `scripts/merge-gate-budgets.json`. Measured on a 31-CPU host at load 40-50
  (2026-11-18), at `GOMAXPROCS=4`: 25-32s for a diff on top of a main this
  host had already linted (17 packages, 50 files; a re-run of one diff 25s),
  and 90s for a fresh edit to the largest package in the module
  (`internal/workflow/runtime/activities/handlers`). In this change's own
  `task ci:merge` (one package, `tools/test-affected`) the phase took 49s and
  ended at +80s, beside a test phase that ran 306s. golangci-lint caches each
  package's analysis, so its time is the changed packages and whatever
  dependencies the cache has not seen. A branch far behind main is the slow
  case: diffs replayed at their own, older, commits took 106-261s at
  `GOMAXPROCS=8`, every dependency a cache miss. The test phase (120s budget)
  is the usual longest phase, so the typical gate's wall time does not grow;
  the far-behind case can, and the summary's GATE-HEALTH line says so. Merge
  main to bring it back.

### Backwards-compatibility checks in the queue

`scripts/check-compat.sh` (the `proto` and `migsafety` phases, only when the
group's diff touches proto or a migration) fails a `buf breaking` change or a
`forge:breaks-rollback` migration unless the owner accepted it with
`breaking:approved` / `migration:breaks-rollback-approved` on the PR: the label's
last `labeled` event must name the owner (`reliant-01`) or the Reliant bot
(`reliant-labs[bot]`) applying it on the owner's instruction (owner decision,
2026-11-19; `COMPAT_OWNERS` overrides the list). With `--queue` it judges only
the entry's own change: HEAD against its sole parent, which must descend from
the group's base. That rests on one assumption about GitHub: a squash-merge
queue builds each entry's `gh-readonly-queue/main/pr-<n>-<sha>` head as ONE
single-parent squash commit on top of the entry ahead of it (or main). When it
does not hold, the script exits 2 naming the parent count or the ancestry it
expected: it refuses, it never passes a break it could not isolate.

**One `buf breaking` finding is additive, not a waiver.** buf's FILE rules
report any change of an rpc's `idempotency_level` (`RPC_SAME_IDEMPOTENCY_LEVEL`),
including an rpc that declared none now declaring `NO_SIDE_EFFECTS` or
`IDEMPOTENT`. That direction changes nothing on the wire: a Connect server also
accepts GET for a `NO_SIDE_EFFECTS` rpc, every existing client (old daemons,
SDKs, Electron, web) keeps sending POST, and no reliant client turns GET on. It
only adds a promise the handler already keeps. So `check-compat.sh proto` runs
`buf breaking --error-format json` and counts a finding as additive only when its
`type` is exactly `RPC_SAME_IDEMPOTENCY_LEVEL` and its message is exactly
`... changed option "idempotency_level" from "IDEMPOTENCY_UNKNOWN" to
"NO_SIDE_EFFECTS"` (or `"IDEMPOTENT"`). It logs those as `additive, not
breaking`. Every other finding still needs `breaking:approved`: dropping or
downgrading `NO_SIDE_EFFECTS` or `IDEMPOTENT` withdraws a promise a client may
already retry or GET-cache on, and `IDEMPOTENT` to `NO_SIDE_EFFECTS` is left out
on purpose to keep the exemption to the one change it was made for. A
run that exits for any reason but findings, a line that is not a finding object,
or no `jq` on PATH exempts nothing. buf's rule stays on: the narrow exemption is
in the gate, so a hand-run `buf breaking` still lists the additive findings.
`scripts/test-check-compat.sh` covers both directions. The vendored control-plane
contract (`proto-vendor/controlplane/`) is outside this buf workspace, so a
re-vendor is judged only by `task contract:controlplane:check` (exactly
`SOURCE`'s export), not by this check; its wire compatibility is control-plane's
`scripts/check-vendored-proto.sh` to enforce.

**Check after the first queue run that reaches it** (the first entry touching
`proto/` or `db/migrations/` after this lands): its `task ci:merge -- --queue`
step logs `check-compat: proto unchanged since <sha>`, `... wire-compatible
with <sha>` or `no migration declares forge:breaks-rollback`, never `the queue
entry's HEAD has N parents` or `is not an ancestor`; and the group head is
single-parent, its parent the previous entry's head (or the group base):
`gh api repos/reliant-labs/reliant/commits/<merge_group head sha> --jq
'[.parents[].sha]'` prints one sha.

**Rollback** if either fails: revert the PR that added the checks (a plain
`git revert` of its squash). The revert touches only `scripts/`, `.github/`, `tools/test-affected` tests and
docs, so the compat phases do not run on it and it cannot be blocked by them;
until it merges, only queue entries that touch proto or migrations are affected.

The fleet's old local queue (`.fm-tools/merge_queue3.sh`), the `merge-gate`
commit status and its audited waiver (`merge_gate_waive.sh`) are retired.

There is no waiver for the release gate: a red release-gate run is fixed, not
waived (`scripts/release-tag.sh --skip-release-gate` is the hotfix escape for
tagging, said in the release notes).

## CI

`.github/workflows/ci.yml` is a thin wrapper over these same commands: every
job is setup, then `task <name>`, so CI cannot disagree with a local run.

| Event | Job | Runs |
|---|---|---|
| `pull_request` (each push) | `Review marker`, `CI result` | nothing heavy: a notice whether `pr-review` passed this head, and the verdict, green in about a minute. Label the PR **`ci:run`** to get the merge gate and the path lanes on it too; it is read on the next push (an empty commit will do), because `ci.yml` does not run on `labeled` (a label run would replace the head's `CI result`) |
| `merge_group` (`merge-queue.yml`) | `Merge gate`, `golangci (changed)`, `vitest (1/4)`..`(4/4)` and `Visual baseline` in parallel, then `CI result` | `Merge gate`: `task ci:merge -- --base <the group's base> --queue --skip-vitest`, `task test:flake` (each new or changed Go test three times), `task ci:repo-checks`, then the five-minute budget check. `golangci (changed)`: `task ci:golangci -- --base <the group's base>`. `vitest`: `scripts/vitest-shard.sh --base <the group's base> --shard <i>/4`. `Visual baseline`: when `scripts/ci-lanes.sh`'s `visual` lane says the group reaches the web UI, `scripts/visual-gate.sh` (the visual suite in the pinned Playwright image, compare only, on the group's commit; [below](#the-visual-baseline-in-the-queue)); otherwise a pass with a notice saying why. `CI result`: passes only when all four succeeded, from `needs` alone. No other path lanes |
| push to `main`, nightly, `workflow_dispatch` | `Release gate (<part>)`, the path lanes, then `Release gate` | each part of the gate as its own job, `task release:gate -- --part <part> [--shard I/N] <sha>` (generate, webbuild, compile, lint, go 1/4..4/4, web; `task ci:repo-checks` in compile), then `Release gate` assembles the part records (`scripts/release-gate.sh assemble`) into the one record. On `main` it publishes it as the git note above, so a CI green counts for `task release:tag`. A missing part or shard fails the assembly closed, and so does a red release build, Playwright or electron smoke (no record) |

### The visual baseline in the queue

Owner, 2026-11-24 (reliant#3446). The visual suite (`frontends/web/e2e/visual`)
was opt-in, and main's baselines went stale two ways: a PR captured on a base
that lacked another PR's UI change, or a later commit on the PR (a pr-fix push)
moved pixels after the capture. The next UI PR then inherited dozens of failures
it did not cause. The merge queue now runs it, compare only
(`--update-snapshots=none`), on the merge group's own commit, main plus the
entries ahead, so both kinds fail on the PR that causes them.

- **When.** `scripts/ci-lanes.sh`'s `visual` lane: what the renderer's production
  build and the suite read (`frontends/web` sources, styles, `public/`, HTML
  entries, vite/tailwind/postcss/tsconfig, `package.json`, the lockfile and pnpm
  patches, the `sdk/ts` sources it aliases, `e2e/visual/`, its config and
  runner), minus unit tests, the other Playwright suites, lint configs and
  docs, plus the lane's own wiring. A group that reaches none of it passes the
  job in seconds with a notice.
- **What it runs.** `scripts/visual-gate.sh`: `task test:web:visual --
  --update-snapshots=none` (the build with the pinned `RELEASE_VERSION`, then
  the suite in the pinned image through `visual-docker.sh`, on the hosted
  runner's docker), then Playwright's JSON report. A capture that differs fails
  the job with a table of surfaces and what differed, the re-capture commands,
  and the diffs as the `visual-results` artifact. A lane that compared nothing
  (no build, no docker, no report, no capture run) fails too: it is never a
  pass.
- **Cost.** Its own runner, beside the gate, so a web group pays only what it
  runs past the gate's other workers. Measured on the hosted runner
  (2026-11-24, two runs): 371s and 381s from the run's creation to the job's
  end (install ~8s, build 64-67s, 166 captures in 4.3-4.5 min with 3 workers),
  166/166 both times. The last 20 web-touching groups' workers ended at a
  median 374s (p25 312s), so the lane adds a median ~16s and at most ~80s to
  a web group's wall time. A group it skips spends ~20s on a runner, beside the
  gate.
- **Flakes.** `retries: 0`, and nothing re-runs. A surface that differs on a
  commit whose UI did not move is deleted with a `flake:removed` issue.
- **The backstop.** `.github/workflows/visual-nightly.yml` runs the same script
  on main every night and keeps one rolling `visual:stale` issue
  (`.github/scripts/visual-stale-issue.sh`): a mismatch opens, reopens or
  comments on it; the next green closes it. It never blocks a merge or a
  release.

### Why the release gate runs as parts in CI

Run in one job, step after step, the gate took 45-70 minutes on a CI runner
(2026-10-30: `test` 1345-2112s, `integration` 591-897s, `lint` 365-549s,
`generate` 201-396s), and every release waited on it. In CI each part is its
own runner, so the gate's wall time is the slowest part's (target ~20 min, plus
the runner queue). Locally `task release:gate` still runs every step in one
process, and records the same way.

CI splits two parts further than the RC train (control-plane
`scripts/rc/rl-gate.sh`, which runs static, lint, go and web) does, and
`assemble` takes either set, because it requires every step, not a fixed list
of parts:

- **static is three parts**, generate (`npm generate docs`), webbuild
  (`npm webbuild demo-size`) and compile (`npm build typecheck vet`). As one part its
  slow steps ran one after another (generate 283s, webbuild 223s, vet 107s on
  2026-11-20) and it was one of the three jobs that set the run's ~15 minutes.
  The webbuild part also replaces the separate `web-bundle` job off a pull
  request: both ran the same `task build:web:verify`.
- **The Go suite is four shards** by package, balanced longest-first on
  measured `go test -race` package seconds. Each go part records its packages'
  seconds (`go_seconds`, read from `test-go.log`), the assembled record carries
  them all, and the next run's `Lanes` job picks the newest earlier record that
  has them (`scripts/release-gate.sh go-timings-source`) and hands that one sha
  to every shard (`RG_GO_TIMINGS_FROM`), so the timings refresh themselves on
  every main run. Each shard records a digest of the timings it split on and
  `assemble` refuses shards that disagree, since some package would then have
  run in no shard. Without a record the shards use the committed
  `scripts/release-gate-go-timings.json` (`scripts/release-gate.sh go-timings >
  scripts/release-gate-go-timings.json` refreshes it). On timings from
  2026-11-10 the three shards ran 587-1030s of tests each.
- **The lint part puts the pinned golangci-lint release on PATH** (the setup
  action's `golangci: "true"`, the binary the merge queue's `golangci
  (changed)` job runs), where forge lint used to build golangci-lint from
  source first, and **restores golangci-lint's analysis cache** from the
  previous main run; main saves it, trimmed to the entries the run used.
  golangci-lint over the whole module was most of the part's 866s, cold every
  time. Its cache is content-addressed, so a warm run lints what changed and
  reports the same findings: on a 48-CPU host, cold 779s, the same commit warm
  136s, four commits later (one touching `internal/workflow/cel`, which most
  packages import) 616s. The key names `.golangci.yml` and `go.mod`: a change
  to either starts cold.

`task test:integration` runs only the packages that have integration-tagged
test files (`scripts/integration-packages.sh`). There are none today: it used to
run `go test -race -tags=integration ./...`, which re-ran the whole unit suite
a second time (the 591-897s above) for no test the unit step had not run.

Beside them run the lanes neither gate covers (`scripts/ci-lanes.sh`,
`task ci:lanes`): the renderer's release build (`task build:web:verify`),
Playwright (`task test:web:e2e`), the electron package
(`task build:electron:smoke`) and the vendored control-plane contract
(`task contract:controlplane:check`, which reads private control-plane with a
deploy key and judges the tree against the control-plane commit its `SOURCE`
declares, never against control-plane's moving main; lag behind main is
`cp-contract-drift.yml`'s issue, not a gate). Main runs all of them (the
contract only when a push changed what it checks; the release build as the
release gate's webbuild part). The merge queue runs those
the diff reaches: the release-gate record does not include them, so the queue
is still where they protect a release. A `ci:run` pull request runs those its
paths reach. `CI result` is the one verdict.

`license-gate.yml`, `pin-drift.yml` and `release-tag-guard.yml` still run on a
pull request that touches their inputs (dependency manifests, the forge pin,
the release scripts): they are path-filtered, informational (not required), and
take seconds to about three minutes.
DB-backed tests boot forge pgtest's embedded Postgres on the runner: there is
no Postgres service container and no `DATABASE_URL`.

