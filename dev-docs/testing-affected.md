# Run only the tests your change can reach

```bash
task test:affected                 # run them
task test:affected:plan            # print what would run, and why, without running it
task test:affected:plan FILES=a,b  # what would these paths run? (a what-if)
```

`task test:affected` diffs your tree against the merge-base with `origin/main`
(committed, staged, unstaged and untracked files all count), works out which
tests can observe each changed path, and runs only those. It prints the reason
next to every selection, so a surprising test is one line away from its cause.

It is the first thing to run before pushing. It does not replace the full lane
(`task test`): that is what the release gate runs, and what a `go.mod` bump
needs. It is also the test step of the **merge gate** (`task ci:merge`), whose
other half is the **release gate** (`task release:gate`) that runs everything
on main before a release: `dev-docs/merge-and-release-gates.md`.

## Who decides what: forge, then reliant

The selection has two owners.

- **forge's affected mode decides the Go tests and the path-gated rows.** It is
  configured in `forge.yaml`'s `ci.gate` (`affected_ignore`, `affected_rules`,
  `affected_readers`, `affected_reach`, `affected_modules`, and `paths` /
  `paths_go_closure` on extra checks). `forge ci gate --plan --json --files
  <changed>` prints its decision: the Go test packages, why each runs, the
  build set, the transitive importers deferred to the release gate, the
  `sdk/go` module's affected packages, and which path-gated rows run
  (electron, docs-generate, migration-order). `forge ci gate test` runs it.
  Load forge's `ci/gate/affected` skill for the semantics.
- **reliant keeps what forge does not have** (owner decision, 2026-11-25), in a
  much smaller `tools/test-affected`: the web lane's vitest selection and its
  merge-tier FULL policy, the forge demo bundle (G11) and the renderer's
  release build, the forge-generate hooks-only narrowing, the per-test time
  budget and gate-verify (G14), and the migration-order check. Its `plan`
  asks forge first (through `scripts/forge.sh`), takes forge's Go and
  path-gated decisions as they are, and adds its own lanes; the merge gate,
  the merge queue's vitest shards and `gate-verify` read that one plan.

`task test:affected` runs `forge ci gate test` (the narrowed `unit-tests` row
as `task test:short -- <packages>`, the `test-mod-sdk-go` row, the flake
check, and the electron, web, web-demo and web-build rows), then
`tools/test-affected run -lanes forge-generate,docs-generate,migration-order`.
`task test:affected LANES=web` runs one of reliant's lanes alone.

## What forge selects (forge.yaml `ci.gate`)

| Changed path | forge's effect |
|---|---|
| a `.go` file | its package, its **direct importers**, and every transitive importer when there are at most 20 more (`affected_reach: {mode: direct, transitive_cap: 20}`); past that the rest are listed as deferred and run in the release gate |
| a `.go` file anywhere | also the packages whose tests walk every Go file of the module (`on: go-source`; `TestWalkerPackagesAreListed` holds the list to the tree) |
| a file a package `go:embed`s or keeps under `testdata/` | that package, fanned out like code |
| `db/migrations/**` | through `db/embed_gen.go`'s embed: `internal/db` and its importers; plus every test package whose test files import `internal/db` or `internal/store/storetest` (`packages_importing`: the DB-backed tests) |
| `proto/**/*.proto`, `proto-vendor/**/*.proto` | its generated Go package (`./gen/{dir}` and its connect subpackage). The regenerated `gen/` files the change commits are Go and fan out from there |
| `sdk/go/**` | the root packages that link it (go.mod replaces it with `./sdk/go`), and the module's own affected tests in a `test-mod-sdk-go` row (`affected_modules: auto`) |
| a non-Go file a Go test names in a path literal (`deploy/kcl/...`) | the packages that name it (`affected_readers: true`), whether or not `affected_ignore` lists the file |
| `go.mod`, `go.sum`, `go.work*`, `tools/tools.go`, `forge.yaml`, `proto/**` that is not a `.proto` | **the whole Go suite** (a rule's `full: true`, with its reason) |
| a file `affected_ignore` lists (docs, CI plumbing, the web, electron and Node trees, the codegen inputs, data) | no Go package, unless a test names it (above) |
| anything else no package owns | **the whole Go suite**, and every reliant lane runs whole too (the fail-safe) |

And the path-gated extra checks:

| Row | Runs when |
|---|---|
| `electron` (`task test:electron`) | a change under `electron/`, the root Node manifests, or a file outside `electron/` its tests read (listed in its `paths`; `tools/test-affected` also selects the lane for any file an electron test names) |
| `docs-generate` (`task generate:docs:check`) | its `paths` (the generators, the files they read, every output) or a file in the build closure of `./tools/docgen/...` (`paths_go_closure`). `TestDocGenTargetsCoverEveryDocsGenerator` holds `paths` to the Taskfile's docs tasks and the generators' sources |
| `migration-order` | `db/migrations/**` |

## What reliant keeps (tools/test-affected)

| Lane | Command it runs | Selected by |
|---|---|---|
| web | `npx vitest run <files>` in `frontends/web/` | every vitest file that imports a changed module (the merge gate: within two edges), the vitest files importing a changed `.proto`'s `_pb.ts`, and tests that read a changed file at run time |
| web-demo | `WEB_BUILD_TARGET=demo npx vite build` (its `vite-plugin-demo-bundle.ts` guard fails on a Reliant-only module in the bundle) | a changed file in the demo bundle's import graph (`WebGraph.Demo`), the demo build's own inputs (`vite.config.ts`, `vite-plugin-*.ts`, `demo.html`), or a FULL web lane |
| web-build | `scripts/web-release-build.sh` (the renderer's release bundle, then `verify-bundle.mjs`) | a FULL web lane (its dependencies, lockfile or build config changed; reliant#2850) or the release build's own inputs (`vite.config.ts`, `vite-plugin-*.ts`, `index.html`) |
| forge-generate | `task generate:check`; in the merge gate only the reached `generate.hooks` when every reason is Go a hook generator compiles (`hooks_only`) | a path forge reads or writes (`forgeInputs` in `rules.go`), or code a hook generator compiles (`go list -deps` of the generators) |
| migration-order | `go run ./tools/test-affected migration-order`: fetches `origin/main` and fails a migration the branch added whose version is at or below main's newest (reliant#1399) | an added or changed file under `db/migrations/` |

The web rules (`rules.go`): a root Node manifest or toolchain pin, the web
config (`frontends/web/package.json`, lockfile, `vite.config.ts`,
`tsconfig*.json`, tailwind/postcss config, `src/test/setup.ts`, patches,
`sdk/ts/package.json`), `sdk/ts/**` and `proto/**` that is not a `.proto` make
the web lane FULL; `frontends/web/src/**` is a web module; served assets and
Playwright specs select only the tests that name them.

The plan (`go run ./tools/test-affected plan -json`) keeps its shape: `go`
(forge's packages, `build`, `deferred`, `modules`), `web`, `electron`,
`forge_generate`, `docs_generate`, `migration_order`, `web_demo`,
`web_build`, `npm_lockfiles`. `-reach direct|all` is the web lane's reach; the
Go lane's is forge.yaml's. `run` keeps `-plan`, `-web-full`, `-web-shard`,
`-test-budget` and `-capture`. `TEST_BASE=<rev>` moves the diff base,
`TEST_FULL=1` runs every lane in full, `-files a,b` asks "what if only these
changed?".

**Lockfiles.** Every lane tests against the `node_modules` already on disk,
so none of them notices a lockfile a frozen install would reject. When a diff
changes a `package.json`, `pnpm-lock.yaml` or `pnpm-workspace.yaml` of a Node
project (`npmProjects` in `plan.go`: the root, `frontends/web`, `electron`,
`tools/protoc-gen-es`), the runner first runs `pnpm install --frozen-lockfile
--lockfile-only --ignore-scripts` there (#1343). forge's `lockfile-<fe>` row
does the same for `frontends/web` in `forge ci gate`.

## The web lane's FULL policy: related files in the merge tier, the whole suite at release

Owner decision (2026-11-13, kept 2026-11-25). A FULL web lane means the rules
cannot scope the change: `frontends/web/package.json`, a lockfile, the vite or
tsconfig config.

- **Merge tier:** only the vitest files the diff's own web modules reach (at
  the merge gate's two-edge cut), beside `tsc -b` over every web source:
  `run -web-full reached` in `scripts/merge-gate.sh` and
  `scripts/vitest-shard.sh`. Speed over completeness.
- **Release gate:** the whole suite, `task test:web` (`npm test -- --run`),
  from `task test`.

forge has no merge-versus-release distinction for a frontend row: its
`test-<fe>` row runs `test:related` with the changed files, and the whole
`test` script when a non-source file under the frontend changed. So the policy
is expressed reliant-side, as before: the merge gate passes `-web-full
reached` to `tools/test-affected`, and the release gate runs the full `test`
script. That is also why the web lane is a reliant extra check rather than
forge's `test-web` row: forge's row would run the whole ~20-minute suite on
every config change in the merge tier.

**Why this and not the alternatives** (decided 2026-11-13):

- **None, as before.** The fallback ran no vitest at all, so a source change
  queued with a change no rule can scope merged untested: #2669's 13 failures
  reached main behind the queue entries for #2677 and #2693, both FULL.
- **The whole suite.** It is the only run that judges the dependency change
  itself, but ~20 minutes is four times the queue's five-minute budget (owner,
  2026-10-28: slow suites gate releases), and the release gate already runs it.
- **The reached files (chosen).** The same selection the lane makes when no
  manifest changed, so a FULL fallback is never weaker than a scoped run of the
  same source diff. What it misses is a dependency bump that breaks a test no
  changed module reaches; that is the release gate's.

## Trade-offs against the old selector

forge's selection is at least as wide as the old `tools/test-affected` Go lane
on every sample measured when the swap landed (a hub package, a migration, a
proto with and without its regenerated code, `sdk/go`, the web config,
`electron/`, a KCL file a test reads, `Taskfile.yml`, `go.mod`), with two
documented narrowings:

- **A `.proto` changed without its regenerated `gen/` code** selects only the
  generated packages' own tests, not their importers: forge's `packages:`
  rule selects test packages and does not fan out. A `.proto` change always
  commits the regenerated `gen/*.pb.go`, which fans out as Go (the same
  selection as before); one that does not fails the forge-generate check,
  which the same change selects.
- **`sdk/go` runs the module's affected packages**, not its whole `./...`:
  forge applies the root module's reach rule inside the module. Its
  `go.mod`/`go.sum` still run the module's whole suite.

The migration lane is wider: forge also runs `internal/db`'s importers
(through the embed), and the packages whose test files import the DB helpers
(`packages_importing`), where the old one ran only the packages its text scan
marked DB-backed.

## How the web selection works, and where it errs

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
  forge's reader scan (`affected_readers`) cannot name. It still runs when its own package or a dependency changes.
  So does one that joins literals onto a root it was handed as a parameter
  (`func readUnder(root string) { filepath.Join(root, "web") }`): a parameter
  is not traced back to its callers, so name the path in the caller, or join it
  onto `repoRoot(t)` where it is read.
- A web test that reads a file through a variable path. The marker scan
  (`readFileSync`, `readdirSync`, `process.cwd()`, `__dirname`) selects any test
  that reads files and names the changed file as a literal.
- **The visual baseline is outside the affected tier, by design.** Its data is
  the shop fixture pack (`frontends/web/src/fixtures/shop/**`), the one world
  the public demo, the hosted preview and the screenshots share, served in the
  page by fixture mode. So an edit to the pack (its story, a handler, an
  overlay in `shop/overlays.ts`) moves visual baselines as surely as a styling
  change does, and no Go or vitest lane here notices: the merge queue's
  `visual` lane (compare only) does, and fails the entry. So a PR that touches
  `src/fixtures/shop/**` or `src/fixtures/{install,outside,transport,overlay}.ts`
  runs `task test:web:visual` itself and commits the baselines it moves
  (`frontends/web/e2e/visual/README.md`).

## Gates and CI: which tests run where

`task test:affected` is the local inner loop. Before a merge and before a
release, the two gates run (`dev-docs/merge-and-release-gates.md`):

| Gate | Go | Everything else |
|---|---|---|
| **merge** (`task ci:merge`) | forge's affected decision (forge.yaml `affected_reach`), `-short`, cached, no `-race`: the changed packages and their direct importers, and the whole closure when it adds at most 20 | the web and electron lanes this selector picks (a FULL web lane: the reached files), `go build ./...`, a compile of every test package (`scripts/test-build.sh`), `forge lint --scope <changed dirs>` (the merge queue too), `forge lint --graph --closed-sets --proto-format` on every diff, the demo build, the renderer's release build on a web dependency or build-config change, and the forge-generate / docs-generate / migration checks when their inputs changed |
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
- **New and changed tests run three times.** `task test:flake`
  (`scripts/test-flake.sh`) runs every Test function the diff adds or edits
  with `-short -count=3`, through forge's flake gate (`forge test flakes
  gate`, which records the run; forge.yaml grades `flake-check: error`). The
  nested sdk/go module's tests, outside the root module forge's gate reads,
  are named by `tools/test-affected changed-tests` and run from that module.
  A test that fails on a repeat is a flake: delete it
  (rewrite it as a new deterministic test if it is worth having), never
  re-run it.
- **The gates' test lanes are recorded.** The merge gate's affected Go tests
  (lane `short`, with coverage) and vitest files (`web-affected`, results
  only), and the release gate's `task test` (`unit`), `task test:web`
  (`frontend`, with v8 coverage), `task test:integration` and `task test:e2e`,
  run through `forge test capture` (`scripts/test-capture.sh`) into
  `.forge/tests/runs/`; `forge test report` summarises a commit's runs and
  main uploads them (ci.yml's `test-upload`). The record never changes a
  verdict; `FORGE_TESTS=off` turns it off. A test that lists the repository
  tree must skip `.forge/` (and hidden directories): each recorded run adds a
  directory there, which would otherwise key its cached result on every run.
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
  agents run builds at once (the merge gate does).

## Changing the selector

What a path selects in Go, and which path-gated rows run, is `forge.yaml`'s
`ci.gate`: add an `affected_rules` entry, an `affected_ignore` glob, or a
row's `paths`, then check it with `task test:affected:plan FILES=<path>`.
A defect in forge's selection is fixed in forge (`ci/gate/affected` skill).
The web rules and the forge-generate inputs live in `tools/test-affected`'s
`rules.go`, with table tests in `plan_test.go`; a change to either selects its
own tests, like any package.
