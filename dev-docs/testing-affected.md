# Run only the tests your change can reach

```bash
task test:affected        # run them
task test:affected:plan   # print what would run, and why, without running it
```

`task test:affected` diffs your tree against the merge-base with `origin/main`
(committed, staged, unstaged and untracked files all count), works out which
tests can observe each changed path, and runs only those. It prints the reason
next to every selection, so a surprising test is one line away from its cause.

It is the first thing to run before pushing. It does not replace the full lane
(`task test`): that is what the release gate runs, and what a `go.mod` bump
needs.

It is also the test step of the **merge gate** (`task ci:merge`), whose other
half is the **release gate** (`task release:gate`) that runs everything on main
before a release: `dev-docs/merge-and-release-gates.md`.

## What runs

| Lane | Command it runs | Selected by |
|---|---|---|
| Go | `go test -short -timeout 5m <pkgs>` (cached, no `-race`) | the changed package, **plus every package whose test binary imports it** |
| web | `npx vitest run <files>` in `frontends/web/` | every vitest file that imports a changed module, transitively |
| electron | `npm test` in `electron/` | any change under `electron/`, or a changed file an electron test names |
| forge-generate | `task generate:check`, first: forge.yaml's pinned `forge generate`, failing (and naming each file) if it changed anything | a change to something forge reads or writes: `proto/**`, `db/**`, a `contract.go` or `doc.go`, `internal/handlers/**` (not its hand-written `_test.go` files, which forge never reads), every generated file (`*_gen.go`, `*_gen.ts`, `*_gen_test.go`, ...), `gen/**`, `deploy/**`, `forge.yaml`, `go.mod`/`go.sum`, `buf.yaml`/`buf.gen.yaml` (`forgeInputs` in `rules.go`) |
| docs-generate | `task generate:docs:check`, second (in a heavy slot): every docs generator (`Taskfile.yml`'s `generate:docs`), failing (and naming each file) if it changed anything | a change to something a docs generator reads or writes (below) |
| migration-order | `go run ./tools/test-affected migration-order`, first: fetches `origin/main` and fails a migration the branch added whose version is at or below main's newest (never applied; the migrator refuses to start on it, reliant#1399). Fails closed when `origin/main` cannot be fetched (`MIGRATION_ORDER_OFFLINE=1` to override) | any added or changed file under `db/migrations/` (a deletion cannot put a file below the head) |

The forge-generate lane is the merge gate's **generate** phase, run locally
(the merge gate runs only the affected `generate.hooks` when nothing forge
itself reads changed; this lane always runs the whole forge generate).
It leaves what it regenerated in your tree, so the fix is to commit those
files. It judges only what generate changes, not whether the tree is clean, so
your uncommitted edits are fine; do not edit files while it runs. Its blind
spot is a `//forge:` directive in a Go file other than `doc.go`.

The docs-generate lane is the gates' **docs** check, run locally: the merge
gate runs `task generate:docs` when the diff reaches a docs input, the release
gate runs the same `task generate:docs:check` on every release, and
`Taskfile.yml`'s `generate:docs` is the one list of docs generators (every task
that runs a `tools/docgen` program or `scripts/generate-mintlify-reference.py`). It exists because a hand edit to a
generated doc passed every local check: #1228 edited the generated
workflow-builder `SKILL.md`, the next regeneration reverted it, and #1230 had to
move the text into `tools/docgen/assembler`. Like forge-generate, it judges only
what regenerating changes and leaves the result in your tree: commit it, or, if
it reverted an edit of yours, put the text in the generator's source. What
selects it (`docGenInput` in `plan.go`):

- **Go a generator compiles**: code (or a `go:embed`ded file) of any package
  in the build deps of a `tools/docgen/*` package, taken from the same
  `go list` graph as the Go lane. `tools/docgen/cli` documents the whole
  command tree, so this is most of the module's non-test code. A `.proto`
  counts through the `gen/` package it regenerates. Test files and
  build-tagged files do not.
- **`docGenPaths` in `rules.go`**: the generators themselves
  (`tools/docgen/**`, the Mintlify script), `go.mod`/`go.sum`, the files they
  read at run time (`config/shortcuts.yaml`, the scenario `types.go`, the
  preset YAMLs, `docs/data/releases/**`) and every output
  (`generated/docs-source/**`, `docs/reference/*.mdx`, `docs/changelog.mdx`,
  `shortcutsData.generated.ts`, `field_descriptions.go`, the workflow-builder
  `SKILL.md`). `TestDocGenTargetsCoverEveryDocsGenerator` checks the list
  against the Taskfile's docs tasks and the generators' own sources, and checks
  that `generate:docs` names every docs generator and that the gates (and
  `ci.yml`) run the list instead of naming a docs task themselves.

`-reach direct` (or `TEST_REACH=direct`) stops the Go fan-out after one hop:
the changed packages and the packages that import one by name. The plan lists
what it left out as `deferred`. The merge gate runs that mode
(`dev-docs/merge-and-release-gates.md`). The default, `-reach all`, is the full
closure described below.

**Reach.** `-reach all` (the default, `task test:affected`) runs every Go
package whose test binary contains a change and every vitest file whose import
closure does. `-reach direct` (the merge gate) runs the changed Go packages and
their direct importers, and the vitest files within two import edges of a
changed module (one edge for a hub, a module more than 60 test files reach in
two, and for every module when together they reach more than 150 at two:
`web.edges`); the rest are listed in the plan as `deferred` and run in the release
gate (dev-docs/merge-and-release-gates.md). The plan's `go.build` lists every
package whose build contains a change, for the merge gate's compile check.

**`run` options.** `-plan <file>` runs a plan an earlier `plan -json` wrote
instead of computing one. `-test-budget auto|<duration>` times every Go test
(`go test -json`) and fails one the diff adds or edits that runs over the
budget, reporting the others (budget.go).

Knobs (environment): `TEST_BASE=<rev>` moves the diff base (a stacked branch),
`TEST_FULL=1` runs every lane in full; `task test:affected LANES=go,web` picks
lanes. For other
flags: `go run ./tools/test-affected -h` (`-json` prints the plan,
`-files a,b` or `-files @list.txt` asks "what if only these changed?").

## The rule table

A changed path is matched against these rules (`tools/test-affected/rules.go`),
and also mapped to its Go package and to the tests that name it. A path that no
rule claims and no Go package owns runs **everything** (fail-safe).

| Changed path | Effect |
|---|---|
| a `.go` file | its package + reverse dependencies (test binary imports it); packages that walk every Go file (`architecture`, `features`, ...) |
| a `_test.go` file | only its own package |
| a Go file behind a build tag (`e2e`, `live`, ...) | nothing: `go test -short` does not build it |
| a file a package `go:embed`s | its package + reverse dependencies, like code |
| a file a Go test names in a path literal (`deploy/...`, `config/...`) | only the packages that name it |
| `db/migrations/**`, `internal/db/migrations/**` | the packages with DB-backed tests (the migrations reach them through `go:embed`, which the import graph does not see). They run in the same `-short` step as everything else: each test copies a migrated template on forge pgtest's server, which needs no configuration |
| `proto/**/*.proto`, `proto-vendor/**/*.proto` | its generated Go package and the vitest files importing its `_pb.ts` |
| `frontends/web/src/**` module | the vitest files importing it (import graph below) |
| `frontends/web/src/**/*.test.ts(x)` | that test only |
| a file a web or electron test reads at run time | those tests |
| `frontends/web/e2e/**` | nothing (Playwright is separate) |
| `electron/**` | the electron lane |
| `docs/**`, `**/*.md`, `.github/**`, `research/**`, lint config | nothing, unless a test names the file |
| `go.mod`, `go.sum`, `tools/tools.go` | **Go: full** (`go.mod`/`go.sum` also run forge-generate and docs-generate) |
| `package.json`, lockfiles at the root | **web and electron: full** |
| `frontends/web/package.json`, `vite.config.ts`, `tsconfig*.json`, `tailwind`/`postcss` config, `frontends/web/src/test/setup.ts` | **web: full** |
| `buf*.yaml` (root) | forge-generate only: the code it generates is in the diff and selects its own tests, or the no-op check fails |
| `Taskfile.yml` | **forge-generate and docs-generate: full** (the tasks they run); the test lanes do not run through the Taskfile |
| `forge.yaml` (the forge pin) | Go and forge-generate: full |
| `proto/**` that is not a `.proto` | **Go and web: full** |
| a deleted web module | the vitest files that still reach it (the graph resolves specifiers against the base tree's files too); its importers changed too and select their own |
| anything else | **everything: full** |

**npm lockfiles.** Every lane tests against the `node_modules` already on disk,
so none of them notices a lockfile `npm ci` would reject. When a diff changes a
`package.json` or `package-lock.json` of an npm project (`npmProjects` in
`tools/test-affected/plan.go`: the root, `frontends/web`, `electron`,
`tools/protoc-gen-es`), the runner first runs
`npm ci --dry-run --ignore-scripts` there (~10s; it validates the lockfile
against the manifest and touches nothing), provided a lane that installs from
that project is enabled. #1343 shipped a lockfile every fresh install rejected
("Missing: zone.js from lock file") with every local test green. The gates and
CI install with plain `npm ci` for the same reason: an `npm ci || npm install` fallback
repaired the drift on the runner and hid it.

## How the selection works, and where it errs

**Go.** `go list -test -json ./...` gives each package's test-binary
dependency set; a package runs when that set contains a changed package. This
is exact for compiled code. The inexact part is data a test reads from disk: the
selector scans each package's Go sources for string literals that name a tracked
repo path (`filepath.Join(root, "deploy", "kcl")`) and selects those packages
when that path changes. It over-approximates on purpose: a test run for no
reason costs seconds, a test wrongly skipped costs a red main.

What counts as naming a path (`scanSources`, `literalCandidates` in
`tools/test-affected/graph.go`):

- **A `filepath.Join` / `path.Join` is judged by its anchor**, the first
  argument, because that is the directory the literals after it are relative to
  (package string constants count as literals):
  - joined from the first argument on, the path is relative to the package
    directory, where `go test` runs: `filepath.Join("..", "..", "frontends", "web", "src")` in
    `internal/grpc` names `frontends/web/src`;
  - joined onto the repo root, it names that path under the root:
    `filepath.Join(repoRoot(t), "scripts", name)` names `scripts/`. The root is
    whatever the package's sources derive from `os.Getwd()`,
    `runtime.Caller`, `filepath.Abs("<relative>")`, a `".."`,
    `git rev-parse --show-toplevel`, or a function, variable or struct field
    that returns or holds one of those (`rootfacts.go`);
  - joined onto anything else (`t.TempDir()`, a fixture's checkout, a parameter)
    it names a directory outside the repo and is **no read at all**:
    `filepath.Join(ws, "web")` builds a fixture's `web` checkout, not a read of
    `frontends/web/`. Before this rule such fixtures made every web-only diff run their
    Go packages.
- **Any other literal is judged alone**: a multi-segment one that is a tracked
  file or directory (`"deploy/kcl/main.k"` in a table) is a read wherever it is
  used, and a bare word (`"web"`, `"db"`) only when it is a file at the repo
  root (`"go.mod"`) or the lone literal argument of another path call
  (`readTree(root, "deploy")`).

**Web.** vitest's own `related` mode builds the same graph by transforming every
test file: `vitest list --changed` took 65 seconds of wall time and 26 seconds of
CPU just to name the files, before running any, on a loaded machine.
`tools/test-affected/webgraph.go` scans quoted
`./x`, `../x` and `@/x` specifiers instead, so the graph takes milliseconds and
`vitest run` gets an explicit file list. Every quoted specifier is an edge, whether
it is an `import`, `import()`, `vi.mock()` or `import.meta.glob`, **except**
`import type` / `export type ... from`, which `verbatimModuleSyntax` erases:
without that, a type import of a hub module (`routeSchemas.ts`) made one
leaf change select 523 of 750 test files. The result was checked against vitest's
own `related` selection for five changed files (`logger.ts`, `transport.ts`, `ChatMessage.tsx`,
`deliveryScope.ts`, a test file): it was a superset every time, with nothing missing
(it also selects the tests that read whole directories of `frontends/web/`, which vitest
cannot see).

**Known blind spots** (both directions are over-selection except these):

- A Go test that builds a path at run time from non-literal parts reads a file
  the scan cannot name. It still runs when its own package or a dependency changes.
  So does one that joins literals onto a root it was handed as a parameter
  (`func readUnder(root string) { filepath.Join(root, "web") }`): a parameter
  is not traced back to its callers, so name the path in the caller, or join it
  onto `repoRoot(t)` where it is read.
- A web test that reads a file through a variable path. The marker scan
  (`readFileSync`, `readdirSync`, `process.cwd()`, `__dirname`) selects any test
  that reads files and names the changed file as a literal.

## Gates and CI: which tests run where

`task test:affected` is the local inner loop. Before a merge and before a
release, the two gates run (`dev-docs/merge-and-release-gates.md`):

| Gate | Go | Everything else |
|---|---|---|
| **merge** (`task ci:merge`) | this selector with `-reach direct`, `-short`, cached, no `-race`: the changed packages and their direct importers | the web and electron lanes this selector picks, `go build ./...`, `forge lint --scope <changed dirs>`, and the forge-generate / docs-generate / migration checks when their inputs changed |
| **release** (`task release:gate`) | `task test`: every package, `-race -count=1`, no `-short`, so every slow test runs; then `task test:integration` and `task test:e2e` | unscoped `forge lint` (golangci-lint, buf, the frontends), `go vet`, `task generate:check`, `task generate:docs:check` |

Nothing the merge gate skips is deleted; it runs in the release gate, and a
release can only be cut from a commit the release gate recorded green.

GitHub CI (`.github/workflows/ci.yml`) is a thin wrapper over the same tasks,
so it cannot disagree with a local run:

| Event | Job | Runs |
|---|---|---|
| `pull_request` | `Merge gate` | `task ci:merge -- --base <the PR's base>`, then `task test:flake` and `task ci:repo-checks` |
| push to `main`, nightly `schedule` (06:17 UTC), `workflow_dispatch` | `Release gate` | `task release:gate -- <sha>`, which records its verdict as a git note (`refs/notes/release-gate`) when the ref is `main`, then `task ci:repo-checks` |

- **The lanes no gate covers** run beside them, selected by
  `scripts/ci-lanes.sh` (`task ci:lanes`): the renderer's release build and
  `verify-bundle` (`task build:web:verify`) on a change under
  `frontends/web/`, the root Node manifests, `electron/release.config.json` or
  `verify-bundle.mjs`; Playwright (`task test:web:e2e`) on `frontends/web/`
  changes that are not unit tests; the electron package (`task
  build:electron:smoke`) on `electron/` or the web build config; and the
  vendored control-plane contract (`task contract:controlplane:check`: the tree
  is the export of the control-plane commit its `SOURCE` declares, and that
  commit is on control-plane main) on `proto-vendor/controlplane/`. Off a pull
  request every lane runs, except the control-plane contract, which judges only
  a push that changed what it checks. control-plane moving ahead is never a
  failure here: `.github/workflows/cp-contract-drift.yml` keeps one
  `cp-contract:drift` issue open while main's copy lags. A change to CI itself (`ci.yml`, `.github/actions/`,
  `ci-lanes.sh`, `Taskfile.yml`) runs every lane.
- **New and changed tests run three times.** `tools/test-affected
  changed-tests` names every Test function the diff adds or edits (by
  function, in files that build without tags); `task test:flake`
  (`scripts/test-flake.sh`) runs them with `-short -count=3`. A test that fails
  on a repeat is a flake: fix it at the root, never by re-running.
- **`CI result`** is the run's single verdict: green when every job that ran
  passed, and the tier's gate ran. On a PR a lane the change does not reach is
  skipped, which is fine; off a PR every lane but the contract must have run.
- **DB-backed tests run in every tier**, `-short` included, and never skip.
  Each test gets its own copy of a template database on forge pgtest's server
  (`internal/db/testutil.go`): an embedded Postgres of the major `forge.yaml`
  declares (`database.postgres_version`), shared by every test process on the
  machine, or `FORGE_TEST_POSTGRES_URL`'s server, which must run that major.
  Tests never read `DATABASE_URL`, and CI runs no Postgres service container.
- **A slow test runs only in the release gate.** A test that takes more than
  about two seconds under `-short` calls `testkit.Slow(t, "<why>")` (or an older
  `testing.Short()` skip). Gating is for tests that are inherently slow: a NATS
  cluster (`*_Cluster_*`), a real subprocess or a daemon that must outlive a
  kill. A test that is slow because it waits out a production timeout or
  backoff should be made fast instead (inject the duration through a
  package-level var or an option) so it keeps running before every merge.
- **To run the release tier before merging**, run the package without
  `-short` (`task test -- ./internal/<pkg>/...`), or run the whole gate on a
  commit: `task release:gate -- <sha>` (`RG_ONLY=test,integration` for a
  subset, never recorded), or `gh workflow run ci.yml --ref <branch>`.
- **A red main is surfaced.** `gate-health.yml` audits main's newest CI run
  after every run on main, and keeps one issue labelled `ci:main-red` open while
  it is red. It also fails unless that run's `Release gate` job ran and passed.
- **license-gate and Pin Drift** (`task licenses`, `task pin:drift`,
  `task pin:ancestry`) run on a PR only when it touches a dependency manifest,
  a lockfile, the pin or their own scripts; on main, always.
- **Caches are saved only on main.** A PR restores main's caches and never
  saves its own: a PR-scoped cache is unreachable from any other ref. The
  `Go package cache` job (pushes to main) saves the `-trimpath` compile of every
  package (`task build:packages`), which every job's `go` reuses in any
  checkout; the release gate saves the embedded Postgres binaries pgtest boots.

## Parallelism and timeouts

- **vitest** (`frontends/web/vite.config.ts`): one worker per core, capped at 8, and the
  5-second test timeout scales 1x to 6x with the one-minute load per core. On an
  idle machine nothing changes. Under load (many agents share the box) a "timed
  out in 5000ms" is the machine, not the code. `VITEST_MAX_WORKERS` and
  `VITEST_TIMEOUT_MS` override both.
- **go test**: `-p` defaults to `GOMAXPROCS`, which is right when one `go test`
  runs. Pass `go run ./tools/test-affected run -p N` to lower it when several
  agents run builds at once.

## Changing the selector

`tools/test-affected` is a plain Go package with table tests
(`plan_test.go`). A change to it selects its own tests, like any package. The
rules live in one table in `rules.go`; add the path there and a test row here.
