# P8 — dev tooling, Taskfile, CI: plan

Status: **plan, not started.** Scope is `PLAN.md` §4 P8. Everything here was
measured against reliant `origin/main` at `cbc5221a`, control-plane `f1b3b7e7`
and forge `df9705c9`, all read-only. The scratch experiments ran in
`/tmp/p8/scratch` (a `git archive` of reliant main). Each claim gives the
command that produced it, so it can be re-run.

## 0. Summary

P8 as written in `PLAN.md` has four parts: make `forge env up dev` in
`../control-plane` the one dev path, delete reliant's own dev stack, replace
the Makefile with a Taskfile, and move CI onto forge's workflows. Most of it
holds up. Three facts found here change the shape, though:

1. **control-plane's `forge env up dev` cannot run where most agents work.**
   It needs k3d (daemon-gateway and workspace-controller run on
   `k3d-control-plane-v2` / `k3d-cp-daemon-v2`: `_on_cp = forge.OnCluster
   {target = _devhost_k8s}`, `deploy/kcl/dev/main.k:2489`). The cloud
   workspaces run in a kata sandbox, where k3d needs a `/dev/kmsg` workaround (verified 2026-10-25; this plan originally said it cannot run k3d). Separately, reliant
   is **public** and control-plane is **private**
   (`gh repo view reliant-labs/{reliant,control-plane} --json visibility`). So
   "control-plane is the only dev path" would leave an outside contributor
   with no dev path at all.
2. **reliant's own dev stack is already broken, so deleting it costs nothing.**
   `scripts/dev.sh` (5 commits ever: `git log --oneline -- scripts/dev.sh`)
   runs `air -c .air.toml` (`args_bin = ["server", "api"]`) plus `npm run dev`.
   It never starts the worker, the gateway, Temporal or NATS, so it cannot run
   a chat under the three-role architecture. The dev path that works today is
   already control-plane's.
3. **reliant's CI is not budget-blocked.** reliant is a public repo, where
   Actions are not billed, and `PR CI` runs green today
   (`gh run list -R reliant-labs/reliant`, 2026-10-22: push and pull_request
   runs `success`). The budget problem is control-plane's. Per the owner's
   rule the local gate is still the merge gate, but for reliant, CI is a
   working second signal that should be kept, not abandoned.

**Recommendation.** Split P8 into **P8a**, which needs no forge F6, and
**P8b**, the D4 ownership move, which needs F6.

- P8a: replace the Makefile with a forge Taskfile. Make `task ci`, which is
  `forge ci gate` plus reliant's extras, the local gate. Rewrite CI as a thin
  wrapper over those tasks and add forge's `proto-breaking.yml`. Have
  DB-backed tests provision their own Postgres through forge `pgtest` instead
  of the 5433 compose server. Delete `scripts/dev.sh` and the rest of the
  legacy stack.
- Dev path (decision **D-P8-1**): give reliant a **host-only
  `deploy/kcl/dev`** of its own (api, worker and gateway on the host; forge
  host infra for Postgres, NATS and the Temporal dev server). It runs in a
  sandbox and in the public repo. control-plane's stack stays the
  full-platform one.
- P8b: once F6 lands, control-plane imports reliant's workload declarations
  instead of hand-listing them, and the two stacks stop duplicating reliant's
  wiring.

## 1. What exists today (verified)

| Thing | Fact | Command |
|---|---|---|
| Makefile | 1,105 lines, **124 targets**. About 40 are dead: they call files that do not exist (`.reliant/scripts/env-loader.sh`, `.reliant/scripts/run-environment.sh`, `docker-compose.test.yml`, `scripts/changelog-helper.sh`, `scripts/changelog-draft.sh`, `test/integration/`, and `schema-validate`, which `ci` depends on) | `grep -oE '^[a-zA-Z0-9_.%-]+( …)*:' Makefile`; `[ -e … ]` per path |
| Taskfile | none on main. `forge project rescaffold Taskfile.yml .github/workflows/ci.yml .github/workflows/proto-breaking.yml` **works** at the pin, after `npm ci` in `tools/protoc-gen-es` (it runs a full generate, 567 files, 3 changed) | scratch run |
| forge gate today | `forge ci gate --list` at the pin: verify-generated, golangci-lint, buf-lint, migration-safety, go-build, vuln-go, license-check, unit-tests, integration-tests (N/A) | `go run …/forge@<pin> ci gate --list` |
| `forge` on PATH ≠ the pin | `reliant forge ci gate --list` **fails** on this machine: `unknown key "lint.tracing"`, `"database.orm"`. The embedded forge is `…20261020131546`, while the pin is `…20261022115939` | `reliant forge version`; `forge.yaml` |
| Dev stack | `scripts/dev.sh` + `dev.js` + `dev.ps1` + `find-ports.js`; ports are scanned dynamically into `.dev-ports.sh` / `.ports.json`; logs go to `./data/logs*`; Postgres is compose `postgres:16-alpine` on 5433 | `rg -n 'air -c\|npm run' scripts/dev.sh` |
| control-plane's dev stack | already runs reliant: `reliant-api-server` / `reliant-temporal-worker` from `../reliant` under `.air.api.toml` / `.air.worker.toml` (`main.k:2830,2882`), the daemon-gateway via `ShellBuild {cwd = "../reliant"}` (`:2908`), reliant-web as a frontend, a `reliant-migrate` job (`:3099`), and Electron behind `-D electron=1` running `npm run dev:electron` in `../reliant/electron` (`:2355+`) | `rg -n '\.\./reliant' deploy/kcl/dev/main.k` |
| Parallel stacks | control-plane keys on the worktree (`option("worktree")`), with `fp.allocate_port(base, key)` handing each worktree a disjoint 100-port block, k3d pre-mapped. `dev_stack.max_stacks: 8` (`forge.yaml:71`) | `main.k:170-250` |
| DB-backed tests | **54 packages** (`rg -l 'SetupTestDB\|storetest.New\|resolveTestDSN\|DATABASE_URL' -g '*_test.go'`). They read `DATABASE_URL` only and never pick a server themselves; `REQUIRE_TEST_DB=1` turns the skip into a failure; `-short` skips them. Two harnesses: `internal/db/testutil.go` (its own template clone) and `internal/store/storetest` (already `pgtest.NewFromTemplate`, but refuses to boot embedded PG) | `internal/db/testutil.go:230`, `storetest.go:75` |
| Test DB in CI | `pr-ci.yml` backend + flake-gate: `services: postgres:16`, `DATABASE_URL=…:5432/reliant`, `REQUIRE_TEST_DB=1` | `pr-ci.yml:885-907,1032-1047` |
| Postgres versions | prod `reliant-pg` is **POSTGRES_16** (control-plane `docs/openbao-secret-store.md:34`); reliant compose is 16; control-plane compose is 17; pgtest embedded is **18.3** (its cached binary, `embedded-postgres-binaries-linux-amd64-18.3.0.txz`; `bootEmbedded` sets no version) | `ls ~/.cache/forge/embedded-postgres` |
| Heavy queue | the Makefile's `SLOT` (`forge slot run --class heavy --`) is identical to forge's Taskfile `SLOT`; the daemon's `internal/heavyjob` already classifies `make test*` **and** `task test*` (`heavyjob.go:85-86`) | — |
| Workflows | 9 files, 4,304 lines; `pr-ci.yml` alone is 1,684 lines (13 jobs, sharded by `tools/test-affected shard`). forge's ci.yml / proto-breaking.yml were scaffolded on 2026-10-12 and then deleted (`.forge/scaffolded.json` → `absent_reported`) | `wc -l .github/workflows/*` |
| Pinned buf | reliant builds buf **v1.57.0** (`Makefile:378`) and hands it to forge as `$BUF`; forge pins buf **1.50.0** (`forge tools pins`) | — |

## 2. Make target inventory (124 → keep / move / delete)

"→ `task x`" means the target moves to the Taskfile under that name. Names
follow control-plane's Taskfile wherever it already has one (`pin:forge`,
`pin:drift`, `pin:ancestry`, `release:*`), so the same muscle memory works in
both repos.

### Build, install, housekeeping (22)

| Make target(s) | Decision | Why |
|---|---|---|
| `build` | → `task build` (forge's; keep the `-X internal/version.*` ldflags from `scripts/release-identity.sh`) | the version stamp is load-bearing (`internal/version/release_identity_test.go`) |
| `build-dev-cli` | → `task build:dev-cli` | `tools/reliant-dev` forensics CLI |
| `build-all` | → `task build:cross` | cross-platform release binaries |
| `build-api-server`, `build-temporal-worker`, `build-tools-daemon`, `build-services` | **delete** | all four build the identical `./cmd/reliant` |
| `docker-build` | **delete** → `forge build --docker` / an optional `task ci:docker` | forge owns image builds |
| `install` | → `task install` | `go install` with ldflags |
| `deps` | → `task deps` (forge's, extended with `web/`, `electron/`, `tools/protoc-gen-es` `npm ci`) | — |
| `setup-hooks` | **delete** | forge configures `.githooks` on the clone (`githooks.go`) |
| `tools` | **delete** → `forge tools install` | forge pins golangci-lint/govulncheck; `@latest` installs are what the gate's pins replace |
| `clean` | → `task clean` | — |
| `clean-all`, `clean-full` | **delete** | they depend on `docker-clean`, which is dead |
| `all`, `dev-build`, `check`, `ci` | **delete** → `task ci` | `ci` depends on the nonexistent `schema-validate` and has not worked |
| `info`, `version`, `help` | **delete** | `task --list`; `reliant --version`; `scripts/release-identity.sh` |

### Tests (26)

| Make target(s) | Decision | Why |
|---|---|---|
| `test` | → `task test` (forge's: `-race -count=1`, under SLOT). **Drop** `make stop`, the call that stops other agents' environments | `reliant.md` already bans `make test` for this reason |
| `test-short` | → `task test:short` (forge's: `-short -timeout 60s`, matching today's) | the scope variable becomes `-- <pkgs>`, which retires `TestMakeTestShortCoversTheRepoByDefault`'s PKG/PKGS bug class |
| `test-affected`, `test-affected-plan` | → `task test:affected`, `task test:affected:plan` (the same `go run ./tools/test-affected`) | reliant-specific; forge's gate affected mode covers far less (§5 FG4) |
| `test-race` | **delete** | `task test` already runs `-race` |
| `test-coverage` | → `task coverage` (forge's) | — |
| `test-e2e` | → `task test:e2e -- ./e2e/stories/` (default path changed; 10m timeout) | needs the Temporal dev server it already starts itself, and a DB from pgtest (§4) |
| `replay-fixtures`, `freeze-replay-fixtures` | → `task replay-fixtures`, `task replay-fixtures:freeze NAME=…` | reliant-specific; the DB comes from pgtest |
| `test-all` | **delete** → `task test:all` | — |
| `test-full-ci` | **delete** → `task ci` | — |
| `test-web`, `test-web-coverage`, `test-web-watch` | → `task test:web`, `task test:web:coverage`, `task test:web:watch` | forge's `test:frontend` globs `frontends/*`, so it never finds `web/` (§5 FG2) |
| `docker-test-build`, `docker-test`, `docker-e2e`, `docker-clean` | **delete** | `docker-compose.test.yml` does not exist |
| `test-env`, `test-integration`, `test-env-integration`, `test-env-start` | **delete** | `.reliant/scripts/*` and `test/integration/` do not exist; forge's `task test:integration` is the integration lane |
| `run-api-test`, `run-web-test`, `run-cli-test`, `run-all-test` | **delete** | dead (env-loader.sh is missing) |

### Lint and format (12)

| Make target(s) | Decision | Why |
|---|---|---|
| `lint` | → `task lint` = pinned `forge lint` | forge wraps golangci-lint + buf + contract lanes |
| `vet` | **delete** | inside forge lint / the gate's go-build |
| `security` | **delete** → the gate's `vuln-go` (`forge ci vuln-scan --go`). gosec is not run anywhere in CI today; re-adding it is a separate decision | the `@latest` installs are unpinned |
| `fmt` | → `task fmt` (goimports + `go mod tidy`) | — |
| `lint-web`, `lint-electron`, `lint-frontend` | → `task lint:web`, `task lint:electron` (`lint-frontend` deleted: run both) | — |
| `fmt-web`, `fmt-electron`, `fmt-frontend` | → `task fmt:web`, `task fmt:electron` (`fmt-frontend` deleted) | — |
| `proto-lint` | **delete** | the buf-lint lane of `forge lint` |
| `proto-format` | → `task proto:fmt` | pinned buf |

### Code generation (28)

Four of these targets are not generators: the aggregates `generate`,
`generate-all` and `generate-go`, plus `generate-refcheck`, which is a check.
That leaves **18 custom generators**: the 4 Go ones (`generate-yaml-bindings`,
`generate-tool-catalog`, `generate-manifest-schema`, `generate-integrations`)
and the 14 `DOCGEN_TARGETS` minus refcheck, plus the control-plane client. The
"~16" in PLAN.md slightly undercounts.

| Make target(s) | Decision | Why |
|---|---|---|
| `proto-generate-reliant` | **delete** | `forge generate` already runs `buf generate` with this exact `buf.gen.yaml`: generate #2 in PLAN §1 failed in that step, and the scratch rescaffold ran "buf generate (Go stubs)" |
| `proto-generate-go` (+ `buf.gen-go-only.yaml`) | **delete** | a subset of the above |
| `proto-generate-controlplane` | → **forge `generate.hooks` entry** `controlplane-client` (`[scripts/proto-generate-atomic.sh, buf.gen.controlplane.yaml, gen/controlplane, web/src/gen/controlplane]`, `writes:` both dirs) | compiled code; it should be covered by `forge ci verify-generated` (D-P8-4) |
| `proto-generate` | **delete** | the two above |
| `generate-yaml-bindings`, `generate-tool-catalog`, `generate-manifest-schema`, `generate-integrations` | → **`generate.hooks`** (`go generate ./internal/workflow/yaml`, `go generate ./internal/llm/tools`, `go generate ./internal/integrations/manifest/schema`, `go run ./tools/integrationgen`), each with `writes:` | compiled code; runs after forge's buf step, which their inputs need; one drift gate (D-P8-4) |
| `generate-schema`, `generate-scenario-schema`, `generate-cel-reference`, `generate-cli`, `generate-tools-ref`, `generate-shortcuts`, `generate-nodes`, `generate-types`, `generate-models`, `generate-presets`, `generate-changelog`, `generate-workflow-builder-skill`, `generate-mintlify-reference` | → `task generate:docs:<name>` each, aggregated by `task generate:docs` (the `DOCGEN_TARGETS` list becomes the `generate:docs` deps list, in dependency order) | docs, not compiled code. Too slow to run on every `forge generate`, and they need python3 |
| `generate-refcheck` | → `task generate:docs:refcheck` (a check, kept in the docs list) | — |
| `generate` | → `task generate` = pinned `forge generate` (proto + forge + hooks), then `task generate:docs` | — |
| `generate-all` | **delete** → `task generate` | — |
| `generate-go` | **delete** | alias |
| `forge-generate-check` | → **`forge ci verify-generated`**, the gate row that already exists. `scripts/check-forge-generate.sh` shrinks to its forge.yaml-vs-go.mod pin check, which moves to an extra check `pin-coherence` | verify-generated regenerates a scratch copy and judges the change, as the script does |
| `docs-generate-check` | → `task generate:docs:check` + a gate extra check `docs-generate` (`scripts/check-docs-generate.sh` invoked with task names instead of make targets) | — |
| `migration` | → `task db:migration NAME=…` (pinned `forge db migration new`) | keeps the pinned migrator |

### Pin, release, docs (15)

| Make target(s) | Decision |
|---|---|
| `pin-forge`, `pin-drift`, `pin-ancestry` | → `task pin:forge`, `task pin:drift`, `task pin:ancestry` (scripts unchanged; `pin:ancestry` is also a gate extra) |
| `release-rc`, `release-patch`, `release-minor`, `release-major`, `release-tag`, `release-tag-dry-run`, `check-release-tags` | → `task release:rc|patch|minor|major|tag|tag:dry-run|check-tags` (scripts unchanged) |
| `changelog`, `changelog-draft` | **delete** (scripts missing) |
| `docs`, `mint` | → `task docs:serve` |
| `docs-build` | **delete** (= `task generate:docs`) |

### Dev environment (21)

| Make target(s) | Decision |
|---|---|
| `dev`, `restart`, `stop`, `stop-legacy`, `status` | **delete**. `stop-legacy` is a `pkill -f vite` / `lsof \| kill -9` that kills other agents' stacks |
| `env-setup`, `env-cleanup`, `env-stop`, `env-info`, `env-validate`, `env-help` | **delete** (dead) |
| `run-api-dev`, `run-web-dev`, `run-cli-dev`, `run-electron-dev`, `run-all-dev` | **delete** → `forge env up dev` (D-P8-1) |
| `dev-web`, `build-web`, `install-web` | `build-web` → `task build:web`; `install-web` folded into `task deps`; `dev-web` **deleted** (the dev stack serves web) |
| `postgres-up`, `postgres-down` | **delete** with the 5433 stack (§4) |

Totals: 22 + 26 + 12 + 28 + 15 + 21 = **124** (every target is named above;
checked by script against the Makefile). **54 move to tasks** (several
merge, e.g. `docs` + `mint`), **5 become `generate.hooks`**, **1 becomes an
existing gate row** (`forge-generate-check`), **1 is folded into `task deps`**
(`install-web`), and **63 are deleted**.

## 3. forge's scaffolded Taskfile vs what reliant needs

From the scratch rescaffold at the pin, the scaffolded file has 27 tasks:
`default deps build test test:short test:frontend test:integration test:e2e
test:all ci ci:checks lint dev dev-up dev-up-prod dev-down proto fmt generate
verify-generate coverage migrate db-seed db-snapshot-restore bench clean
dev-psql`.

| forge task | For reliant |
|---|---|
| `default`, `test:short`, `test:integration`, `test:all`, `ci`, `ci:checks`, `coverage`, `bench`, `clean`, `migrate` | **keep as scaffolded.** `migrate` already says `go run ./cmd/reliant db migrate up` |
| `test` | keep. Decide the per-package timeout: forge 120s, today's `make test` 300s. Start at forge's and gate what overruns (control-plane did the same with `FORGE_RENDER_PKGS` / `SUBPROCESS_PKGS`) |
| `deps`, `build`, `fmt`, `lint`, `test:e2e` | **edit**: web + electron + protoc-gen-es installs; release ldflags; the `./e2e/stories/` default |
| `test:frontend` | **replace** with `test:web` + `test:electron` (the glob misses `web/`; FG2) |
| `generate` | **replace**. The scaffold runs a bare `buf generate` + `go mod tidy` after checking for `protoc-gen-go` on PATH. reliant's plugins are `go tool` directives, so that check is wrong here, and it skips forge generate entirely (FG2) |
| `verify-generate`, `proto` | **delete** (duplicates of `forge ci verify-generated` / forge generate's buf step) |
| `dev`, `dev-up`, `dev-up-prod`, `dev-down`, `dev-psql`, `db-seed`, `db-snapshot-restore` | **delete** (they assume a project docker-compose with an `app-debug` profile; reliant's dev is `forge env up dev`) |
| every `forge …` call | **pin it**: `FORGE: GOWORK=off go run github.com/reliant-labs/forge/cmd/forge@<forge.yaml forge_version>`. Bare `forge` on PATH resolved to a different forge here and rejected forge.yaml (§1; FG2b) |

What reliant adds: `test:affected[:plan]`, `test:web[:coverage|:watch]`,
`test:electron`, `lint:web`, `lint:electron`, `fmt:web`, `fmt:electron`,
`build:dev-cli`, `build:cross`, `build:web`, `install`, `generate:docs[:<n>]`,
`generate:docs:check`, `replay-fixtures[:freeze]`, `db:migration`,
`proto:fmt`, `pin:*`, `release:*` and `docs:serve`. Also new in `forge.yaml`:
`generate.hooks` (5 entries), `ci.gate.extra_checks` (§5) and
`ci.gate.affected_ignore` (`["*.md", "docs/**", "research/**",
"dev-docs/**"]`).

## 4. How tests get a database once 5433 is gone

Options:

| | Option | Verdict |
|---|---|---|
| A | **forge `pgtest` shared embedded server** (default), `FORGE_TEST_POSTGRES_URL` to opt into an external one | **Recommend** |
| B | Point tests at the control-plane stack's 5434 | No: real data, and the rule is never to touch it |
| C | Keep a reliant compose Postgres for tests only | Keeps docker as a test prerequisite and a server that everyone on the box shares. That is what P8 removes |

Why A works for reliant:

- pgtest already does the hard parts. `AcquireShared` shares one embedded
  server per user across every test binary, worktree and agent, with a
  cross-process refcount and teardown by the last user. Migrations run once
  per migration hash and tests get `CREATE DATABASE … TEMPLATE` clones.
  `FORGE_TEST_TEMPLATE_TTL` reaps stale templates. No docker is needed, which
  matters in the sandbox. `storetest` already uses `NewFromTemplate`. The only
  thing that keeps reliant off the embedded server is its own refusal
  (`storetest.go:75-78`, `testutil.go:256`).
- The rationale for that refusal goes away. It exists because "a default
  makes a bare `go test` create and drop databases on whatever answers at
  that address". The embedded server is the test run's own, not a server
  someone else chose.
- It also removes a real hazard. With tests reading `DATABASE_URL`, a dev
  shell that exported the stack's `DATABASE_URL` (5434) points `go test` at
  real data. After the move, tests never read `DATABASE_URL`; it is process
  configuration only.

Changes (lane L3):

1. `internal/db/testutil.go`: replace its template-clone and DSN-resolution
   code with `pgtest.NewFromTemplate` (or `storetest.New`), so there is one
   harness. Keep the seeded project/user and the throwaway vault.
2. `storetest.New`: drop the `DATABASE_URL` fallback and the refusal. Server =
   `FORGE_TEST_POSTGRES_URL` if set, otherwise embedded.
3. `e2e/stories/main_test.go` and `replaytest/harness_gen_test.go`: same.
4. `REQUIRE_TEST_DB` is retired: with embedded as the default, "no server" can
   only mean a boot failure, and that is always a test failure. Its CI role
   (#464: DB tests silently skipped while CI was green) is covered by
   construction. `forge ci verify-test-run` can additionally assert that the
   tests ran.
5. **D-P8-3**: DB-backed tests under `-short`. They are hermetic and
   cache-sound (the migrations come in through `go:embed` in package
   `reliant/db`), so run them in `task test:short` and `task ci`. Measure
   first; anything over ~2s gets a slow-test gate.
6. Delete `internal/config/compose_postgres_port_test.go` (it pins the 5433
   port across the Makefile, compose and scripts) and the 5433 hints in
   `provideTestDBHint`.
7. CI: keep the `postgres:16` service container, exported as
   `FORGE_TEST_POSTGRES_URL`, **until FG1 lands**. Embedded is 18.3 and prod is
   16, and a test suite that passes on 18 does not prove the SQL on 16.
   Locally, the 18-vs-16 gap is acceptable in the interim.

## 5. CI

Constraints: reliant's Actions run (public repo). The org's 20 concurrent jobs
are shared with control-plane. The owner's rule is that the local gate is the
merge gate. A GitHub App token cannot push `.github/workflows/*` (PLAN "1h"),
so a human with workflow scope lands this lane.

**The local gate: `task ci` = pinned `forge ci gate all` + reliant's
`ci.gate.extra_checks`.**

| Extra check | Command | Notes |
|---|---|---|
| `test-affected` | `go run ./tools/test-affected run -lanes go` | `replaces: unit-tests` until FG4. forge's affected mode falls back to the whole suite on any non-Go change and has no web/electron/codegen lanes |
| `web` | `test-affected run -lanes web` + `npm run lint` + `npm run build` + `.github/scripts/verify-bundle.mjs` | until P9 declares `frontends:` (then forge's `deps-web`/`lint-web`/`typecheck-web`/`build-web` rows take over) |
| `electron` | `test-affected run -lanes electron` | — |
| `docs-generate` | `scripts/check-docs-generate.sh` | `heavy: true` |
| `pin-coherence` | forge.yaml `forge_version` == go.mod forge (from `check-forge-generate.sh`) | cheap |
| `pin-ancestry` | `scripts/check-pin-ancestry.sh` | `cache: false` (network) |
| `migrations` | `scripts/check-migrations.sh` | goose-era rules over goose's files; deleted with goose (2026-10-22, runbook 04 §9) |
| `npm-licenses` | `scripts/check-npm-licenses.sh` | forge `license-check` covers Go only |
| `release-config`, `gh-scripts` | `node .github/scripts/sync-release-config.mjs --check`, `node --test '.github/scripts/*.test.mjs'` | — |
| `cp-contract` | proto-vendor vs control-plane main | `cache: false`, `skip_exit_code: 77` when the private sibling is unreachable (an outside contributor) |
| `no-binaries` | `.githooks/check-binary-files-ci.sh` | — |

**Workflows.**

| File | Decision |
|---|---|
| `pr-ci.yml` → `ci.yml` | **Rewrite as a thin wrapper** (D-P8-5). Keep the topology that earns its cost: `changes` (test-affected plan + shard matrix), `checks` (`task ci:checks`), Go shards (`task test:affected -- -shard i/n`), web/vitest shards, e2e-backend, e2e-frontend, electron, and the `ci-result` gate. Every job body becomes one `task` call. That removes ~1,200 lines of inline YAML logic and the drift between CI and the local gate |
| `proto-breaking.yml` | **adopt forge's** (rescaffold). New capability: reliant has no `buf breaking` today, and control-plane consumes `reliant.v1` |
| `license-gate.yml`, `check-migrations.yml` | **delete**: folded into the gate |
| `pin-drift.yml` | keep only its weekly drift schedule (Tier-2); ancestry is a gate row |
| `release.yml`, `release-tag-guard.yml`, `release-health.yml`, `gate-health.yml`, `build-images.yml` | keep (Tier-2, reliant-owned). Update `release.yml`'s `make generate-all` / `make generate-changelog` mentions to `task` |
| forge's `build-images.yml` / `deploy.yml` / `e2e.yml` | do **not** adopt. reliant deploys only through control-plane, and its own `build-images.yml` serves that |

## 6. What changes in control-plane's `forge env up dev`

Little, because it already runs reliant as host processes. Things it uses from
reliant that P8 must **keep**: `.air.api.toml`, `.air.worker.toml`,
`scripts/air-run-worker.sh`, `electron/scripts/wait-and-start.js` +
`npm run dev:electron`, `dist/reliant` (built by the electron ShellBuild),
`.github/scripts/` (control-plane's reliant-sibling gate steps, `ci-gate.sh:563+`),
and `cmd/reliant` subcommands, ports and env names (rollout rule 3).

control-plane edits (lane L6, small):

1. `reliant_web_base`'s `env_file = "../reliant/.env"`: reliant tracks no
   `.env`, and the KCL already sets every `VITE_*` it needs (`main.k:3255+`).
   Drop the `env_file` once L5 deletes reliant's `.env` convention.
2. Comments that name reliant's Makefile, `dev-electron` or `scripts/dev.sh`
   (`Taskfile.yml:1570`, `main.k` electron header, `scripts/build-reliant-image.sh:43`)
   should name `task`.
3. Nothing in control-plane runs `make` in reliant (`rg 'make ' …` finds only
   comments), so the Makefile deletion breaks nothing there.
4. **P8b only (after F6):** replace the hand-listed `wl.reliant_api_server` /
   `wl.reliant_temporal_worker` / `wl.daemon_gateway` env with imports of
   reliant's `deploy/kcl/workloads.k` + `config_gen.k` (D4).

## 7. Fix-in-forge list

| # | Gap | Evidence | Blocks |
|---|---|---|---|
| FG1 | `pgtest` embedded Postgres has no version knob: 18.3, while prod is 16. Pin the embedded major to a project declaration (e.g. `database.postgres_version` in forge.yaml, which `forge env up` host infra can share) | `bootEmbedded` sets no `.Version`; cached 18.3 binary | dropping CI's service container (§4.7) |
| FG2 | The scaffolded Taskfile is wrong for an in-place adopter. `test:frontend`/`deps`/`fmt` glob `frontends/*` instead of the declared `frontends:` paths. `generate` runs bare `buf generate` and checks for `protoc-gen-go` on PATH instead of running `forge generate`. `verify-generate` and `proto` duplicate forge's own steps. `dev-up`/`dev-down`/`dev-psql` assume a compose dev app the project may not have | scratch rescaffold | nothing (reliant edits its own copy), but every adopter hits it |
| FG2b | Scaffolded tasks call bare `forge`, which can be a different forge from the pin: here `reliant forge` rejected forge.yaml. The Taskfile should resolve the pinned forge, or forge should re-exec the pin when it is not the pin (the merge driver already does this) | §1 | L1 correctness |
| FG3 | One buf per project. forge pins buf 1.50.0 while reliant needs 1.57.0 and smuggles it in through `$BUF`. Let forge.yaml pin tool versions, or bump forge's | `forge tools pins`; `Makefile:378` | L1 (drops the Makefile's buf bootstrap) |
| FG4 | Affected selection exists three times: forge's gate (Go only; full fallback on any non-Go path), reliant `tools/test-affected` (4,375 LOC: Go, web import graph, electron, codegen lanes, migrations → DB packages) and control-plane `tools/ci-affected`. Upstream one engine with project rules (`ci.gate.affected.rules`) | `wc -l tools/test-affected/*.go` | not blocking (`replaces: unit-tests`) |
| FG5 | `forge ci gate test` cannot shard (reliant CI runs up to 6 weighted shards) | `pr-ci.yml:117` | not blocking |
| FG6 | = PLAN F6: cross-project KCL import of workloads + `config_gen.k` | PLAN §5 | P8b |
| FG7 | forge's `ci.yml` `integration-test` job requires `docker info`, though testkit/pgtest need no docker | rescaffolded `ci.yml` | nothing |

## 8. Lanes (disjoint files)

```
FG1, FG2b, FG3 (forge, released + pinned) ──┐
L3 test DB ─────────────────────────────────┼─► L1 Taskfile + forge.yaml ─┬─► L2 CI (.github, human) ─┐
L8-spike standalone dev ─► L8 deploy/kcl/dev ┘                            └─► L4 docs ──────────────────┼─► L5 deletions ─► L6 control-plane
                                                                                                        │
                                                                         (after F6) P8b: D4 ownership ◄──┘
```

| Lane | Files (owned exclusively) | Depends on | Verify |
|---|---|---|---|
| **L3 test DB** | `internal/db/testutil*.go`, `internal/store/storetest/**`, `e2e/stories/main_test.go`, `internal/workflow/runtime/replaytest/harness_gen_test.go`, `internal/config/compose_postgres_port_test.go` (delete) | nothing; FG1 only for CI's switch | `go test ./internal/db/... ./internal/store/...` with **no** `DATABASE_URL` set (they must run, not skip); full lane with `FORGE_TEST_POSTGRES_URL` at a pg16 |
| **L1 Taskfile** | `Taskfile.yml` (new), `Makefile` (every kept target becomes a one-line `task` forwarder for one merge, rollout rule 3), `forge.yaml` (`generate.hooks`, `ci.gate.*`), `scripts/check-forge-generate.sh`, `scripts/check-docs-generate.sh`, `tools/test-affected/{main.go,rules.go,docgen_test.go,plan_test.go}` (make → task argv; the `Makefile` rule becomes `Taskfile.yml`; parse the docs list from the Taskfile), `internal/config/makefile_test_short_test.go` → `taskfile_test_short_test.go` | FG2b (or a local pinned-forge var), FG3 (or keep `$BUF`) | `task ci` green; `forge ci verify-generated` no-op; `make <old>` still works via the forwarders |
| **L2 CI** | `.github/workflows/**` | L1 | the workflow-dispatched full tier green; paste a `task ci` table in the PR |
| **L4 docs** | `reliant.md`, `CONTRIBUTING.md`, `.reliant/skills/dev-logs/SKILL.md` (one stack, not two; drop `./data/logs` and 5433), `.reliant/tools/{port-info,log-search,inspect-state,api-call}.sh` (read `forge env config`, not `.dev-ports.sh`/`.ports.json`), `dev-docs/{testing-affected,pinning}.md`, `contributing/CONFIG_REFERENCE.md`, `electron/README.md`, `buf.gen.yaml` header comment | L1, D-P8-1 | links and commands resolve |
| **L5 deletions** | `scripts/{dev.sh,dev.js,dev.ps1,find-ports.js,dev-multi-instance.sh,list-instances.sh,logs.sh,dev-electron.js,dev-electron-supervise.sh,verify-dev-setup.sh,test.sh,test-all.sh,test_setup.sh,db-context.js}`, `.air.toml`, `.air.windows.toml`, `docker-compose.yml` (D-P8-6), `docker-compose.cloud.yml` (D-P8-6), `Makefile`, root `package.json` scripts (`dev*`, `instances*`, `logs`, `find:ports`, `clean:ports`, `proto:*` → `task`, `start:backend*` (the dead `api --port`)), `electron/scripts/{dev-multi.js,setup-ports.js,kill-ports.js,dev-with-ports.sh}` + `electron/package.json` `dev`/`dev:clean`/`dev:fresh`, the `.gitignore` entries, stale comments (`web/vite.config.ts:140`, `cmd/reliant/cmd/login_helpers_test.go:17`, `electron/scripts/with-release-config.mjs:31`, `electron/electron-builder.local.js`, `proto/reliant/config/v1/daemon_config.proto:76` + regenerate) | L1–L4 merged and the new path proven | `rg 'dev\.sh\|dev-ports\|data/logs\|5433\|find-ports\|make '` clean outside research/ and the autopilot builtins |
| **L6 control-plane** | control-plane `deploy/kcl/dev/main.k` (env_file, comments), `Taskfile.yml` comment, `scripts/build-reliant-image.sh` comment | L5 | control-plane `task ci`; `forge env render dev` unchanged except the env_file |
| **L8 standalone dev** (if D-P8-1 = b) | reliant `deploy/kcl/dev/main.k` + `deploy/k3d.yaml` absent (host-only), `secrets/dev.yaml` | a **spike** first: can api (`AUTH_MODE=apikey`), worker and gateway run with no control-plane (daemon token validation, LLM keys)? | `forge env up dev` in reliant starts api + worker + gateway + web; one chat round-trips with a local daemon |

**Not P8.** The zero-reference scripts (`build-electron-cross-platform.sh`,
`capture-pprof.sh`, `dev-web-prod.sh`, `dogfood-*.sh`, `onboarding-trace.sh`,
`setup-auth.sh`, `setup-hooks.sh`, `start-temporal-ui-cli.sh`,
`test-cache-locally.sh`, `post-rebase-hook.sh`, `convert-jwks-to-pem.go`).
Zero references does not prove nobody uses them. Delete one in L5 only if it
reads 5433, `./data/logs` or `.dev-ports.sh`. Also leave alone
`internal/workflow/builtin/autopilot-*.yaml`'s `make test-short` fallback: it
is product behavior for users' repos, not reliant's tooling.

## 9. Risks

| Risk | Mitigation |
|---|---|
| **No dev path in the sandbox or for the public repo** if control-plane's k3d stack is the only one | D-P8-1 (b), lane L8 |
| **Many concurrent dev instances on one machine.** Today: `find-ports.js` scans for free ports with no ceiling. After: control-plane allocates one 100-port block per worktree, `max_stacks: 8`, with k3d pre-mapped ranges, and each instance is a whole platform stack (admin-server, k3d namespace, gateway pod). There are 175 worktrees under `~/.reliant/worktrees/reliant-labs` on this machine; only running stacks hold blocks, and `forge env devstack prune` reclaims dead ones | For reliant's own L8 stack, use the same `fp.allocate_port(base, option("worktree"))` primitive. A host-only stack has no k3d pre-map, so its ceiling can be high (or off). Document `forge env devstack list/prune` in the dev-logs skill |
| Two dev stacks wire reliant's env twice until F6 | P8b; until then L8's KCL and control-plane's must use the same env names (typed config makes drift a lint failure: `enforce_typed_access: error`) |
| Embedded PG 18 hides a PG-16-only SQL failure (or vice versa) | CI keeps pg16 via `FORGE_TEST_POSTGRES_URL` until FG1 |
| Tasks run the wrong forge (PATH ≠ pin) | FG2b; until then the Taskfile's `FORGE` var is `go run …@pin` |
| Tests coupled to the Makefile (`TestDocGenTargetsCoverEveryDocsGenerator`, `TestMakeTestShortCoversTheRepoByDefault`, `compose_postgres_port_test.go`) and the test-affected `global-config` rule go red mid-migration | they move in the same PR as the file they read (L1/L3) |
| Workflow edits need a human | D-P8-7 |
| `generate.hooks` always run, so a slow hook taxes every `forge generate` | only the compiled-code generators become hooks (`go generate` of four packages + one buf template; seconds). The docs generators stay tasks |
| Deleting `docker-compose.yml` removes the only runnable self-host example | D-P8-6 |

## 10. Decisions for the owner

| # | Decision | Options | Recommendation |
|---|---|---|---|
| **D-P8-1** | The dev path | (a) control-plane's `forge env up dev` only (PLAN as written); (b) that **plus** a host-only `forge env up dev` in reliant | **(b).** (a) needs the `/dev/kmsg` workaround in the kata sandbox (originally written as "cannot run"), and it gives the public repo no dev path. (b) is also D4's shape: reliant declares its workloads and control-plane binds them. Devil's advocate: two stacks to maintain. That cost is real until F6 lets control-plane import reliant's declarations, and then the duplication goes away |
| **D-P8-2** | Test database | pgtest embedded by default, tests stop reading `DATABASE_URL`, vs. keep the explicit-DSN contract | **pgtest embedded**, `FORGE_TEST_POSTGRES_URL` as the only override |
| **D-P8-3** | DB tests under `-short` | run them vs. keep skipping | **run them** (hermetic, cache-sound); gate any measured >2s |
| **D-P8-4** | Where the 18 generators live | all Taskfile `generate:*` (PLAN's wording), vs. hybrid | **hybrid**: the 5 compiled-code generators become forge `generate.hooks`, so `forge generate` is one command and `verify-generated` is one drift gate; the 13 docs generators + refcheck become `task generate:docs` with a gate row |
| **D-P8-5** | CI shape | adopt forge's 4-job `ci.yml` verbatim, vs. a thin wrapper that keeps reliant's sharded affected topology | **thin wrapper**, until FG4/FG5 let forge's own `ci.yml` express affected sharding |
| **D-P8-6** | `docker-compose.yml` / `docker-compose.cloud.yml` (they hold the 5433 Postgres **and** a self-host api/worker/gateway/daemon example; no doc links either) | delete both, vs. move the self-host part to `examples/self-host/` | **delete both**, unless a compose self-host is a deliverable you want to keep supporting |
| **D-P8-7** | Who lands `.github/workflows/**` | — | a human with workflow scope lands L2 |
| **D-P8-8** | Makefile cutover | hard cut, vs. one merge of `make → task` forwarders | **forwarders for one merge** (rollout rules 3/6), deleted in L5 |
