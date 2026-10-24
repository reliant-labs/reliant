# Run only the tests your change can reach

```bash
make test-affected        # run them
make test-affected-plan   # print what would run, and why, without running it
```

`make test-affected` diffs your tree against the merge-base with `origin/main`
(committed, staged, unstaged and untracked files all count), works out which
tests can observe each changed path, and runs only those. It prints the reason
next to every selection, so a surprising test is one line away from its cause.

It is the first thing to run before pushing. It does not replace the full lane
(`go test ./...`, `npm test`): that is what a release or a `go.mod` bump needs.

It is also the test step of the **merge gate** (`task ci:merge`), whose other
half is the **release gate** (`task release:gate`) that runs everything on main
before a release: `dev-docs/merge-and-release-gates.md`.

## What runs

| Lane | Command it runs | Selected by |
|---|---|---|
| Go | `go test -short -timeout 5m <pkgs>` (cached, no `-race`) | the changed package, **plus every package whose test binary imports it** |
| web | `npx vitest run <files>` in `frontends/web/` | every vitest file that imports a changed module, transitively |
| electron | `npm test` in `electron/` | any change under `electron/`, or a changed file an electron test names |
| forge-generate | `make forge-generate-check`, first: forge.yaml's pinned `forge generate`, failing (and naming each file) if it changed anything | a change to something forge reads or writes: `proto/**`, `db/**`, a `contract.go` or `doc.go`, `internal/handlers/**`, `*_gen.go`, `gen/**`, `deploy/**`, `forge.yaml`, `go.mod`/`go.sum`, `buf.yaml`/`buf.gen.yaml` (`forgeInputs` in `rules.go`) |
| docs-generate | `make docs-generate-check`, second (in a heavy slot): every docs generator (the Makefile's `DOCGEN_TARGETS`), failing (and naming each file) if it changed anything | a change to something a docs generator reads or writes (below) |

The forge-generate lane is CI's **forge generate is a no-op** check, run
locally. CI runs that check on main only (it alone cost a PR 110-160s), so
running this lane before pushing a proto, migration or handler change is how a
PR finds out.
It leaves what it regenerated in your tree, so the fix is to commit those
files. It judges only what generate changes, not whether the tree is clean, so
your uncommitted edits are fine; do not edit files while it runs. Its blind
spot is a `//forge:` directive in a Go file other than `doc.go`.

The docs-generate lane is the docs half of CI's **Generated code is up to
date** job, run locally: that job runs the same `make docs-generate-check`, and
`DOCGEN_TARGETS` in the Makefile is the one list of docs generators (every
target that runs a `tools/docgen` program or
`scripts/generate-mintlify-reference.py`). It exists because a hand edit to a
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
  against the Makefile recipes and the generators' own sources, and checks
  that `DOCGEN_TARGETS` names every docs generator and that CI runs
  `make docs-generate-check` instead of naming targets itself.

`-reach direct` (or `TEST_REACH=direct`) stops the Go fan-out after one hop:
the changed packages and the packages that import one by name. The plan lists
what it left out as `deferred`. The merge gate runs that mode
(`dev-docs/merge-and-release-gates.md`). The default, `-reach all`, is the full
closure described below.

Knobs (environment): `TEST_BASE=<rev>` moves the diff base (a stacked branch),
`TEST_FULL=1` runs every lane in full, `LANES=go,web` picks lanes. For other
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
| `buf*.yaml` | **everything: full** |
| `Taskfile.yml`, `Makefile` | **forge-generate and docs-generate: full** (the tasks they run); the test lanes do not run through the Taskfile |
| `forge.yaml` (the forge pin) | Go and forge-generate: full |
| `proto/**` that is not a `.proto` | **Go and web: full** |
| a deleted web module | **web: full** (its importers can no longer be found) |
| anything else | **everything: full** |

**npm lockfiles.** Every lane tests against the `node_modules` already on disk,
so none of them notices a lockfile `npm ci` would reject. When a diff changes a
`package.json` or `package-lock.json` of an npm project (`npmProjects` in
`tools/test-affected/plan.go`: the root, `frontends/web`, `electron`,
`tools/protoc-gen-es`), the runner first runs
`npm ci --dry-run --ignore-scripts` there (~10s; it validates the lockfile
against the manifest and touches nothing), provided a lane that installs from
that project is enabled. #1343 shipped a lockfile every fresh install rejected
("Missing: zone.js from lock file") with every local test green. PR CI installs
with plain `npm ci` for the same reason: an `npm ci || npm install` fallback
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

## CI tiers: which tests run where

`make test-affected` is the local inner loop. GitHub's PR CI
(`.github/workflows/pr-ci.yml`) runs on every PR, every push to main, nightly
(06:17 UTC) and on manual dispatch. The event picks the tier:

| Event | Tier | Go | Everything else |
|---|---|---|---|
| `pull_request` | **fast**, job `Backend (Go, short)` | `go test -short` over the packages **this selector** picks for the PR's diff | only the lanes the diff can reach (below) |
| push to `main`, nightly `schedule`, `workflow_dispatch` | **full**, job `Backend (Go, full)` | `go test` over every package | every lane |

The PR tier is built to finish in about five minutes, and selection is how:
the org's runners are shared, so a job a PR does not need is a slot another PR
waits for. Nothing a PR skips is deleted; it runs on the next push to main.

- **What decides a PR's lanes.** The `Detect what changed` job runs this tool
  (`plan -base <merge base>`) for the Go package and vitest file selection, and
  path patterns (in the job's script) for the rest: lint and the e2e stories
  run unless the change is confined to `frontends/web/`, `electron/`, docs, research or
  `.github/`; codegen drift runs unless it is confined to the frontends,
  electron, research or `.github/` (the generated `frontends/web/src/gen/` and
  `shortcutsData.generated.ts` still count); the web build runs on `frontends/web/`, the
  root Node manifests, `electron/release.config.json` or `verify-bundle.mjs`;
  Playwright on `frontends/web/` changes that are not unit tests; the electron package on
  `electron/` or the web build config. Its job summary prints the decision.
- **A PR that changes `pr-ci.yml` runs everything**, so a CI change is proven
  against the whole PR path it changes.
- **New and changed tests run three times.** `tools/test-affected
  changed-tests` names every Test function the diff adds or edits (by
  function, in files that build without tags); the `New and changed Go tests
  (x3)` job runs them with `-count=3`. A test that fails on a repeat is a
  flake: fix it at the root, never by re-running.
- **`CI result`** is the run's single verdict (green when every job that ran
  passed; a lane the change skipped is fine) and its summary shows each job's
  queue wait and run time.

- **DB-backed tests run in both tiers**, `-short` included, and never skip.
  Each test gets its own copy of a template database on forge pgtest's server
  (`internal/db/testutil.go`): an embedded Postgres of the major `forge.yaml`
  declares (`database.postgres_version`), shared by every test process on the
  machine, or `FORGE_TEST_POSTGRES_URL`'s server, which must run that major.
  Tests never read `DATABASE_URL`, and `REQUIRE_TEST_DB` no longer means
  anything: a server that cannot boot fails the test.
- **A short-gated test runs only on main and nightly.** Gating is for tests that
  are inherently slow: a NATS cluster (`*_Cluster_*`), a real subprocess or a
  daemon that must outlive a kill. A test that is slow because it waits out a
  production timeout or backoff should be made fast instead (inject the
  duration through a package-level var or an option) so it keeps running on
  every PR.
- **To run the full tier before merging**, run the package without `-short`
  locally (`go test ./internal/<pkg>/...`), or start PR CI on your branch with
  `gh workflow run pr-ci.yml --ref <branch>`.
- **A red main is surfaced.** `gate-health.yml` audits main's newest PR CI run
  after every run on main, and keeps one issue labelled `ci:main-red` open while
  it is red. It also fails unless every `Backend (Go, full) N/M` shard, 1 to M,
  ran and passed on main.
- **The tiers cannot silently merge.** The job name carries the tier, so the two
  have separate check histories. `TestCITestTierReachedGoTest`
  (`tools/test-affected/ci_tier_test.go`) fails when the job's declared tier
  (`RELIANT_TEST_TIER`) disagrees with whether `-short` reached `go test`.
- **The Go tests run in up to 6 shards** (`Backend (Go, short) 1/2`, ...).
  `go list ./... | go run ./tools/test-affected shard` prints the matrix: as
  few shards as keep each under its weight target (one for a PR that reaches a
  handful of packages, six for the whole module), balanced by measured
  duration in `tools/test-affected/go-test-weights.tsv`, slowest first. Every
  package runs in exactly one shard whatever the weights say; a stale weights
  file only costs balance. `CI result` fails when a shard did not pass (and on
  main when none ran). Every test job restores ONE package cache, which the
  main-only `Go package cache` job produces: the compiled non-test packages
  every test binary imports, and nothing else (no test variants, which a PR
  invalidates by construction). ~540 MB compressed, restored in ~10-15s; the
  old everything-cache was 3.2 GB and cost 1-4 minutes per job. The codegen
  job keeps its own cache, because the Makefile builds generators with
  `-trimpath`, a different compile key. The same job saves the module cache
  without its `.zip` files (a build reads only the extracted trees), so a
  PR's `go test` fetches a module only when go.sum moved. Refresh the weights when a shard
  runs much longer than the others: `gh run view <run-id> --log | go run
  ./tools/test-affected weights > tools/test-affected/go-test-weights.tsv`.
- **The test Postgres runs without durability** (fsync, synchronous_commit
  and full_page_writes off; pgtest's embedded server is configured that way):
  per-test template databases made the DB-backed packages fsync-bound
  (internal/db 166s -> 54s). The short tier also passes `-vet=off`: vet, like
  golangci-lint, runs on main.
- **Other jobs**: `Lint (forge)` (at the pinned forge; blocks). On a PR:
  `go build ./...` (every package compiles) and forge's fast gating lanes over
  the whole project (`forge lint --no-fix --contract --migration-safety
  --generated-drift --create-nullability --computed-fields --read-only-fields
  --tracing --graph`, ~12-40s). golangci-lint and buf lint run on main only:
  golangci-lint took 4-7 minutes even scoped to a few central packages. Run
  `forge lint` locally before pushing Go or proto changes. Web
  lint/typecheck/build, vitest (`Web (vitest K/N)`, one runner per 150
  selected files, up to four), Playwright (one runner), the e2e stories, and
  `Generated code is up to date` (`make docs-generate-check`, the
  docs-generate lane above, then `make generate-all` drift; on main also
  `forge generate is a no-op`, the forge-generate lane above).
- **license-gate and Pin Drift** run on a PR only when it touches a dependency
  manifest, a lockfile, the pin or their own scripts; on main, always.
- **Caches are saved only on main.** A PR restores main's caches and never
  saves its own: a PR-scoped cache is unreachable from any other ref.

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
