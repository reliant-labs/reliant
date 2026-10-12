# Reliant → Forge migration plan

Status: **approved** 2026-10-10 — D1, D2, D4 as recommended; D3 revised by the
user: **sqlc does not survive long-term** (it may stay transitionally in P5a).
Shared context: `00-BRIEF.md` (same dir).
Scope (user): contract code, config system, scaffolded mocks, dev tooling,
observability, frontend styling — broad forge adoption, not a narrow port.

Evidence below comes from running forge against a scratch copy of reliant
(`/tmp/forge-mig-scratch`, `git archive HEAD` of branch `migrate-reliant-to-forge`),
not from reading forge's source. Commands are quoted so each claim can be re-run.

---

## 1. What running forge on reliant actually did (verified)

| Step | Result |
|---|---|
| `reliant forge project new --in-place --name reliant --mod github.com/reliant-labs/reliant --disable frontend` | **Succeeded** (1m51s). Wrote 38 new paths, **kept 15 existing files** untouched (`.air.toml`, `Dockerfile`, `buf.yaml`, `buf.gen.yaml`, `.golangci.yml`, 2 workflows, README, …), merged `.gitignore`, no git init. Detected `web/` and printed the exact opt-in: `frontends: [{name: web, type: vite-spa, path: web}]`. |
| New paths | `forge.yaml`, `Taskfile.yml`, `cmd/reliant/cmd/` (forge cobra tree: `root.go server.go db.go version.go commands.go services/ workers/ operators/`), `db/` (`migrations/ queries/ seeds/ sqlc.yaml`), `deploy/` (`k3d.yaml kcl/ observability/`), `pkg/middleware/`, `proto/config/v1/config.proto`, `proto/forge/`, `gen/go.mod` (**gen becomes a separate module**), `.github/workflows/{ci,pre-commit,proto-breaking}.yml`, `.githooks/`, `docs/{runbook,slo}.md`. |
| `forge generate` #1 | **Refused before writing anything**: 3 existing `contract.go` are non-canonical — `accesstokenclient/client.go:43`, `gitcredentialclient/client.go:36` (constructor not `func(Deps) Service`), `integrations/ghusers/contract.go:38,43` (`TokenSource`/`User` instead of `Service`/`Deps`). |
| `forge generate` #2 | Failed at buf: reliant's kept `buf.gen.yaml` needs `tools/protoc-gen-es` installed (`npm ci`). Forge rolled back cleanly and preserved output under `.forge/failed-generate/`. |
| `forge generate` #3 | Failed at composition: `cliauth` and `forgecred` (CLI-only contract packages) have Deps (`credentials.Dirs`, `oauth2.HTTPDoer`, `func(string) error`, `Exchanger`, …) with no server provider. Forge assumes **every contract package is a server component**. Workaround: `//forge:external-component`. |
| `forge generate` #4 | Got through proto/handler/mock/config codegen, then failed at `go mod tidy` because **`/var/cache/workspace` (shared Go cache volume) is 100% full** — environment, not forge. |
| What #4 produced before tidy | **All 36 services discovered from `proto/reliant/v1/*.proto` as-is** → `internal/handlers/<svc>/` with `service.go` + one `rpc_<name>.go` stub per rpc (`svcerr.Wrap(svcerr.ScaffoldStub(...))`, importing `pb ".../gen/reliant/v1"`) + per-rpc scaffold tests; per-service typed mount subcommands in `cmd/reliant/cmd/services/`; `internal/handlers/mocks/*_mock_gen.go`; for existing contract packages `mock_gen.go` + `middleware_gen.go` + `tracing_gen.go` + `metrics_gen.go`; `pkg/config/config_gen.go`; `deploy/kcl/config_gen.k`; `secrets/dev.yaml`. The generated `internal/runs/mock_gen.go` (`MockRepository` with `contractkit.Recorder` + `XxxFunc` fields) shows the existing contracts are already close to forge's shape. |
| `forge lint --conventions` | 50 findings: **45 `forgeconv-list-filter-optional`**, 2 `internal-package-missing-contract`, 2 `deps-are-interfaces` (`cliauth.Deps.Dirs`, `mcpserver.Deps.Limiter`), **1 `one-service-per-file`** (`workflow.proto`: WorkflowService + ScenarioService). |
| `forge lint --contract` | Could not run (disk full). |

Takeaway: forge can already ingest reliant **in place, with today's proto layout**. The
blockers found are a handful of renames and markers plus three real forge design gaps (§5).

## 2. Size of the job (measured)

| Area | Today | Count |
|---|---|---|
| RPC handlers | one Go package `internal/grpc/services` | 241 files, **39.7K LOC**, 36 services, 307 rpcs; mounted in `internal/grpc/server.go` (36 `New*Handler` calls) + daemon `ToolsDaemonService` in `internal/grpc/daemon_server.go`, `internal/toolexec/daemonruntime/server.go` |
| Error mapping | hand `connect.NewError` | **1,290** non-test call sites in `internal/` |
| Process config | `os.Getenv`/helpers spread over 25 packages | **~85 env vars** (64 via `os.Getenv/LookupEnv`, 138 call sites; +~20 via `serverEnvOrDefault*`/`envutil`/`envInt`); heaviest: `internal/llm` 22, `cmd/reliant` 17, `internal/features` 12. control-plane's dev KCL sets 21 of them for reliant. |
| Contracts | 12 `contract.go`, ~292 `interface` decls | 104 internal packages |
| DB | goose (146 files, 12 `StatementBegin`, 7 `NO TRANSACTION`, `goose.WithAllowMissing()` at `internal/db/connect.go:208`) + sqlc (`pgdb`, `database/sql`, 33 query files, hand `schema.sql`) + hand store layer | 63 tables |
| CRUD-name collisions | rpcs named `Create/Get/Update/Delete/List<E>` whose `<E>` has a table + message | **52 of 165** CRUD-shaped rpcs (Chat, Project, Worktree, Task, Plan, Trigger, Setting, ModelEndpoint, Connection, Workflow, Message, Attachment, …) |
| Public rpcs | hand list `publicMethods` (`internal/grpc/server.go:143`) | 9 procedures |
| Proto validation | none | 0 files use `buf.validate` |
| Frontend | Vite 7 + React 19 + Tailwind 4 + TanStack | 688 vitest files, 10 Playwright specs, 20 `VITE_*` build-time vars, 7 color schemes via `data-color-scheme` + `.dark` |
| Temporal | workflows/activities | 31 files with `workflow.Context` |

## 3. Decisions (recommendations — need your sign-off)

### D1 — In-place, not `reliant-next`. **Recommend in-place.**
- Evidence: §1 — `--in-place` adoption works, keeps every existing file, and never git-commits.
- `-next` breaks control-plane, which `require`s `github.com/reliant-labs/reliant` and imports `pkg/llmcatalog` and `gen/reliant/v1[/reliantv1connect]`. With 320K LOC under heavy parallel development, a side-by-side repo would be a months-long fork.
- Devil's advocate: in-place means two serving pipelines (`internal/grpc` and forge's `Serve`) coexist for a while, and the first `generate` drops 307 stub rpcs into the tree at once. Mitigations: cut over one service at a time inside the existing server (§4 P3), delete the old pipeline as soon as the last service moves, and fix the "adopt all services at once" gap in forge (§5 F4).

### D2 — Keep `proto/reliant/v1` and package `reliant.v1` for now. **Recommend: don't move protos.**
- Evidence: forge discovered and scaffolded all 36 services from `proto/reliant/v1/*.proto` unchanged. The only layout lint is `workflow.proto` holding 2 services.
- Moving to `proto/services/<svc>/v1/` is possible without changing wire names (`api.proto_package: reliant.v1`, which control-plane already does with `controlplane.v1`). But `go_package` would become `gen/services/<svc>/v1`: one Go package turns into 36. That rewrites every `pb.` reference across ~2,500 files plus control-plane's imports, and the 24 cross-file imports (9 from `common.proto`) become cross-package imports. That's a lot of churn for no capability.
- Do: split `ScenarioService` into its own file in the same package; fix the 45 `optional` list filters (adding `optional` to a proto3 scalar is wire-compatible, but changes Go/TS types to pointers).
- Devil's advocate: staying off the canonical layout risks forge features that assume it. Mitigation: treat any such assumption as a forge bug (§5), since forge already supports a fixed package.

### D3 — Database. **DECIDED: forge migrations + migratekit + pgtest now. sqlc is transitional and gets removed in favor of the forge ORM (P5b).**
- **User decision (2026-10-10): don't keep sqlc long-term.** It may survive P5a so the migration cutover isn't coupled to a data-layer rewrite. The end state has no `sqlc.yaml`, no `pgdb` package and no `db/queries/`.
- End-state data layer:
  - Forge-generated entity structs, `<Entity>Store` and ORM delegates for every table, projected from the applied schema.
  - Queries the generated store can't express go in the owned `internal/db/<entity>_repo_ext.go` seam, using `orm.QueryOption` filters and raw bun via `db.Bun()`. Bun handles CTEs, `FOR UPDATE SKIP LOCKED` and `NewRaw` natively.
  - Transactions are scoped with `RunTransactionWithOptions` / `orm.Context` (`forge project libraries orm`).
- **No one has dogfooded the forge ORM at this scale yet.** control-plane is `//forge:no-orm` (`control-plane/internal/db/doc.go:11`, a hand-owned pgx repository). reliant becomes the first serious consumer, so expect ORM gaps to surface. Fix them in forge (F11–F12), never by bringing sqlc back.
- Transitional (P5a only): forge's scaffold wrote `db/sqlc.yaml` with `schema: db/migrations/`. Pointing sqlc at the migrations already retires the hand-maintained `schema.sql`, `scripts/generate-schema-sql.sh`, `schema_drift_test.go` and `make generate-schema` before the ORM swap lands.
- Migration engine: goose → golang-migrate via `forge/pkg/migratekit` (`schema_migrations` + `schema_migrations_applied/compat`, advisory lock, embedded FS, initContainer, `AutoMigrate` for the desktop/monolith boot path).
- **Behavior change:** reliant applies out-of-order migrations (`WithAllowMissing`). Forge refuses them (`MissingMigrationError`) and requires `forge db migration rebase`, backed by the PR-time lint `migration-version-below-merged-head`. Forge's rule is more correct, since schema shouldn't depend on merge order. Accept it.
- History: **squash to a baseline** at cutover. Pre-launch we owe nothing to the 146-file history, and squashing also removes the goose-specific `StatementBegin`/`NO TRANSACTION` handling. Existing DBs (prod `reliant_cp`, staging, dev 5434, users' desktop DBs) need a one-time "goose → migratekit adopt" step (§5 F1).
- ORM/CRUD: P5a may start with `//forge:no-orm`; P5b removes the marker. Turning the ORM on makes the **52 CRUD-name collisions live**: forge would wire generic `pkg/crud` ops over rpcs like `GetChat`, `UpdateChat` and `ListMessages`, which carry real business logic. Each one must either become forge CRUD on purpose, with owned overrides (`db/crud-overrides`: `op.Filters/Fetch/Persist` in `handlers_crud.go`), or opt out explicitly. That makes F5 a **P5b blocker**, not a nice-to-have.
- Devil's advocate on dropping sqlc: forge's own `db` skill says "Complex: write SQL in `db/queries/` and use sqlc". reliant's hot paths (message streaming, computed state, run-control locks, `SKIP LOCKED`, recursive thread/workflow cascades) are hand-tuned SQL that sqlc type-checks at generate time, and raw bun doesn't. The answer: hand SQL stays hand SQL, in `_repo_ext.go` via bun. The compile-time checking sqlc gave is recovered by running every `_repo_ext.go` query against pgtest's real migrated postgres in tests. Forge's guidance and scaffold should change to match (F11).

### D4 — Dev and deploy ownership. **Recommend: reliant owns its declarations, control-plane owns the bindings.**
- Today control-plane already runs reliant: `deploy/kcl/workloads.k:611+` declares `reliant_api_server` / `reliant_temporal_worker` / `daemon_gateway` with hand-typed commands, ports and probes. `deploy/kcl/dev/main.k:2508-2800` launches `go run ./cmd/reliant server api|worker` from `../reliant` and sets 21 env vars by name. reliant's own `scripts/dev.sh` stack (Postgres 5433, `./data/logs`) is a second, divergent path.
- Target: reliant's typed `AppConfig` (projected to `deploy/kcl/config_gen.k`) and its workload definitions live in reliant. control-plane imports them and binds them to environments, images and secrets, so it stops hand-listing reliant's env names. Then delete reliant's `dev.sh`/compose/`.dev-ports`/`data/logs` path.
- Interim: until cross-project KCL import is proven (open — §5 F6), control-plane keeps owning everything and reliant skips `deploy/kcl/<env>`. Env-var names stay identical, so nothing in control-plane breaks.
- Devil's advocate: reliant never deploys on its own; it ships only inside the platform, plus desktop and self-hosted daemon. Owning `deploy/` could look like ceremony. The config schema is the part that matters: today it drifts silently between reliant's `os.Getenv` calls and control-plane's KCL.

## 4. Phased plan

### Rollout rules: piece by piece, never break anything (user directive, 2026-10-11)

Every change ships as its own **small PR** that leaves everything that works today still working. A PR that can't meet all of these gets split further:

1. **One concern per PR.** Small enough to review and revert alone. Never a big-bang `forge project new --in-place` commit. §1 showed that one command adds 307 placeholder handlers, a nested `gen/go.mod`, new CI workflows and git hooks, and a second cobra tree at once.
2. **reliant stays shippable at every merge.** `go build ./...`, `make test-short` for touched packages, the web build, and CI all stay green.
3. **control-plane never breaks.** It `require`s reliant and imports `pkg/llmcatalog`, `pkg/daemonwire`, `gen/reliant/v1`, and `gen/reliant/v1/reliantv1connect` (re-check with `rg -o 'reliant-labs/reliant/[a-z0-9/_-]+' control-plane --type go | sort -u` before any move). Those import paths, the `reliant.v1.*` wire names, and every env var / command / flag / port that `control-plane/deploy/kcl/{dev/main.k,workloads.k}` relies on (`reliant server api|worker|gateway`, `reliant db migrate up`, `HEALTH_PORT`, `GRPC_PORT`, `RELIANT_*`) keep working. A change that must move one lands **compatibly first** (old and new both work), then control-plane moves, then the old path is deleted in a third PR.
4. **The shared `forge env up dev` stack never breaks.** Air configs, `go run ./cmd/reliant server api|worker`, log locations and ports stay valid at every merge.
5. **Deployed data is never at risk.** Anything touching a live schema or migration bookkeeping (goose → migratekit) ships with an idempotent, tested adoption path and rolls out dev → staging → preprod → prod. It is never the same PR as other changes.
6. **Old paths are deleted only after the new one is proven** in its own PR (reliant: no backwards compatibility pre-launch, but no flag-day either).
7. **Forge fixes land in forge first** (released + pinned), then reliant adopts them. reliant never carries a workaround for a forge gap we can fix.

### Status (updated 2026-10-12)

**Merged** (CI budget exhausted; each change was verified locally before merging, and forge main was re-verified with all of them combined):

| Item | PR |
|---|---|
| P0 unblock (disk freed, spawning works) | — |
| F11 sqlc removed from forge + `generate.hooks` escape hatch | forge #641 |
| migratekit run-lock race (affected every forge project) | forge #648 |
| F1 goose adoption (`-- forge:adopts goose …`); runbook `04-p5a-cutover-runbook.md` | forge #649 |
| F8/F9 in-place adoption keeps go.mod's forge pin; buf plugin pre-flight | forge #646 |
| F4 adopt one service at a time (`// forge:unmanaged`) | forge #650 |
| F2 CLI/daemon-only contract packages (`//forge:not-a-component`) | forge #651 |
| F3a `binary_config` must name a real binary | forge #653 |
| F3b role-scoped config (`binary_config.command`) | forge #654 |
| 1a canonical contracts | reliant #740 |
| 1b ScenarioService split (wire-identical) | reliant #741 |
| P6 wave 0: dead packages / deps-are-interfaces / exclude-contract on 21 pure packages | reliant #743 / #744 / #745 |
| Dev CORS: reliant now gets `CORS_ALLOWED_ORIGINS` (was a no-op `CORS_ORIGINS`) | control-plane #841 |

**Next:** a forge release, then pin it in reliant (and control-plane). Then slice 1g: a minimal reliant `forge.yaml` (every service `// forge:unmanaged`, `cliauth`/`forgecred` `//forge:not-a-component`, sqlc as a `generate.hooks` entry), then 1h (`forge lint` in CI). The dev DBs are approved to be destroyed and rebuilt, so P5a skips goose adoption for dev; it is still required for prod.

Research inputs: `02-contracts-classification.md` (P6) and `03-config-census.md` (P4), both in this dir.

### The P1 slice, as separate PRs

| # | PR | Repo | Breaks nothing because | Depends on |
|---|---|---|---|---|
| 1a | Rename the 3 non-canonical `contract.go` (`accesstokenclient`, `gitcredentialclient`, `integrations/ghusers`) to forge's `Service`/`Deps`/`New(Deps)` shape | reliant | Pure internal refactor; no exported API used outside reliant | — |
| 1b | Split `ScenarioService` out of `workflow.proto` into `scenario.proto`, same package | reliant | Same package, `go_package`, service and procedure names; only the file moves. Go/TS generated symbols are identical | — |
| 1c | **F4** forge: a project can mark proto services forge doesn't manage (yet), so adopting forge doesn't scaffold 307 stubs + mounts over handlers that live elsewhere | forge | New opt-in; default unchanged | — |
| 1d | **F2** forge: contract packages that aren't server components (CLI-only `cliauth`/`forgecred`) don't fail composition | forge | New opt-in or derived; default unchanged | — |
| 1e | **F1** forge: migratekit adopts a goose-managed database (reads `goose_db_version`) idempotently | forge | New capability; existing projects untouched | — |
| 1f | **F8/F9** forge: `--in-place` keeps go.mod's pinned version; pre-flight the project's own buf plugins | forge | Bug fixes | — |
| 1g | reliant `forge.yaml` (minimal, all services unmanaged) + Taskfile mirroring Makefile targets. **No** `gen/go.mod`, **no** cmd tree swap, **no** new CI workflows/hooks yet | reliant | Additive files only; Makefile, CI and `cmd/reliant` untouched; `forge generate` only regenerates what Make already does | 1a, 1c, 1d, forge release + pin |
| 1h | `forge lint` added to reliant CI as **non-gating**, then gating once green | reliant | Advisory first | 1g |

Then P2 onward proceed one service, one package or one concern at a time under the same rules.

**Corrections from research (supersede §2 numbers):**
- 106 top-level `internal/*` units (214 leaf packages), not 104.
- `db.Repository` is ONE 442-method interface. 156 methods have no caller outside `internal/db`, and 31 packages consume it; the big consumers are `grpc/services` (179 methods), `activities/handlers` (74), `llm/tools` (52) and `reconciliation` (23). It must never get a generated mock; it's split into consumer-side role interfaces (02 §2).
- 147 env reads across ~100 names. control-plane sets 41 names on reliant processes, in KCL (`dev/main.k`, `lib/env.k`, `prod/main.k`) AND in Go (`workspace_controller.go:2016-2318`); the rule 3 check must cover both (03 §0).
- **Live bug:** control-plane dev sets `CORS_ORIGINS`, reliant reads `CORS_ALLOWED_ORIGINS`, so dev CORS is `*` (03 §1f). Fixing it enforces CORS on the shared dev stack, so it waits on a user decision.
- Forge gap **G1:** `forge lint --contract` passes everything when there's no `forge.yaml`, so per-package contract gates only mean something after slice 1g.
- Forge gap **G6 (reported):** passing two lane flags (`--conventions --contract`) silently runs only one lane and still prints ✅. Until it's fixed, always run lint lanes **separately**. Marker refusals (`forge-exclude-contract-*`) only show under `--contract`.

**P0 — Unblock (now).** Free the `/var/cache/workspace` volume (100% full; this blocks every Go build on the box). Fix agent spawning: every sub-agent spawn here failed with `project path not set, cannot load presets` (bug filed). Pin the forge version: `forge.yaml forge_version` must equal go.mod's `v0.1.44-0.20261007193810-e75c74afb46e`; in-place wrote the running binary's newer version instead.

**P1 — Land forge in place.** Superseded by the P1 slice table above. The end state is unchanged:
- reliant has a `forge.yaml`.
- Contracts are canonical.
- `gen/` decided: recommend staying in the root module until a separate PR proves control-plane builds against a nested `gen/go.mod`, since control-plane imports `gen/reliant/v1`.
- forge's `cmd/reliant/cmd` tree merged with `cmd/reliant/commands` via `userCommands(deps)`, in its own PR.
- Taskfile becomes the entry point, with Makefile targets delegating until P8.

**P2 — Proto hygiene (S).** Depends on P1. Owns `proto/`, `gen/`, `web/src/gen`.
- (Split `ScenarioService` out of `workflow.proto`: moved to P1 slice 1b.)
- Make the 45 list filters `optional`, fixing Go/TS call sites. One PR per proto file; it's wire-compatible but changes Go/TS field types.
- Declare the 9 public rpcs with `auth_required: false`, replacing the `publicMethods` slice.
- Optionally add `buf.validate` rules + `forge/pkg/validate`.

**P3 — Handlers onto forge's `*Service` model (L, the biggest phase).** Depends on P2.
- P3a (M, serial): extract the shared helpers in `internal/grpc/services` (`chat_helpers.go`, `daemon_proxy_base.go`, `daemon_choice.go`, `log_redact.go`, …) into real packages, so each service can move independently.
- P3b (L, **parallel per service**; each lane owns `internal/handlers/<svc>/` + its source files in `internal/grpc/services/`): move each service's methods onto `internal/handlers/<svc>/Service`. In `internal/grpc/server.go`, swap that one `New*Handler` call for the forge handler. Delete the scaffold stubs as each lands.
- P3c (M): replace `internal/grpc/server.go` + `internal/serverapi|serverworker|servergateway` with forge's `Serve` + `ServeSpec` per role:
  - `server api` = subset `Mount`;
  - `server worker` = Temporal worker as a supervised component;
  - `server gateway` = the NATS bridge;
  - `reliant server` = monolith (all services and workers).
- reliant's extra interceptors (timeout, domain whitelist, machine-wake, slow-rpc watchdog, error reporter) join `observe.Chain` as named `Deps`.

**P4 — Typed config + secrets (M).** Depends on P1. Owns `proto/config/`, `pkg/config`, the `os.Getenv` sites.
- Declare the ~85 vars in `proto/config/v1/config.proto` with **today's env-var names** (explicit `env_var:`), so control-plane KCL needs no change.
- Mark secrets `sensitive: true` (DB URL, NATS creds, Supabase/Sentry/Statsig keys, JWT keys, `INTERNAL_SERVICE_SECRET`, …). Dev values go in `secrets/dev.yaml`.
- Migrate call sites package by package (parallel lanes by package), then flip `enforce_typed_access: error`.
- Daemon and CLI configs are separate messages loaded with `config.Load` (flags > env > file > defaults). The product's user config (`config/reliant-schema.json`, `~/.reliant`) stays bespoke.
- Per-role scoping needs F3.

**P5a — Migrations + DB test infra (M).** Depends on P1. Owns `db/` and `internal/db/` migration/test infra only.
- Squash to a baseline `db/migrations/<ts>_baseline.up.sql` and switch to migratekit + `AutoMigrate`.
- Ship the goose→migratekit adopt step (F1) and roll it to dev → staging → preprod → prod and desktop.
- **Transitionally**, point sqlc at `db/migrations`; delete `schema.sql` + its generator + the drift test; add `//forge:no-orm`. Since F11, `forge generate` no longer runs sqlc implicitly, so reliant declares it as a `forge.yaml` `generate.hooks` entry (`command: [sqlc, generate]`, `writes: [internal/db/postgres/generated]`) until P5b deletes it.
- Move DB tests to `forge/pkg/pgtest`/`testkit`. This removes the `DATABASE_URL`/shared-5433 rule; measure the speed first.
- `forge lint --migration-safety` on; `forge db seed` replaces `dogfood-prep/reset` where it fits.

**P5b — Replace sqlc + the hand store layer with the forge ORM (L).**
- Depends on P5a, on F5 (CRUD-collision opt-out) and F12 (ORM gaps found in the spike), and on P3b for each service whose handlers call the store. Owns `internal/db/**`, `internal/db/postgres/**` and `db/queries/`.
- **Spike first (S):** port the hottest, hairiest paths (message streaming + `message_content_blocks`, computed chat state, `run_control_lock`, thread/workflow cascades, `SKIP LOCKED` queues) to generated stores + `_repo_ext.go` bun queries. Benchmark them against today's sqlc versions; every gap becomes an F12 forge fix.
- Remove `//forge:no-orm`; `forge generate` projects entity structs/stores for all 63 tables.
- **Decide the 52 CRUD collisions explicitly:** forge CRUD with owned overrides where generic semantics fit (likely settings, presets, model endpoints, plans, tasks, triggers), opted out where they don't (chats, messages, worktrees, projects carry real logic).
- Port the 33 `internal/db/postgres/queries/*.sql` files and the `*_store.go` layer table by table. Lanes run in parallel by aggregate (chat/message/thread, workflow/run, project/worktree/repo, trigger, connection/connector, tokens/auth, settings/presets/model-endpoints). Each lane owns its `_repo_ext.go` files plus the query/store files it deletes.
- Collapse `internal/db/core` domain types into the generated entities where they duplicate them. Keep real domain types where they carry behavior.
- Finish: delete `sqlc.yaml`, `db/sqlc.yaml`, `internal/db/postgres/generated` (`pgdb`), `db/queries/`, `make sqlc`, the sqlc buildmode/array-param tests and the sqlc tool pin. Every `_repo_ext.go` query has a pgtest test (this replaces sqlc's generate-time SQL checking).
- Interaction with P6: the narrow consumer-declared interfaces now sit in front of generated stores instead of `pgdb.Querier`. Do P6's Repository split against the post-P5b shape, or the interfaces get written twice.

**P6 — Contracts + generated mocks (L, highly parallel).** Depends on P1; handler packages wait for P3.
- Set `contracts: {strict: true}` in warn mode.
- Classify all 104 packages: service / strategy / adapter (`forge:outbound-io`) / pure (`//forge:exclude-contract: <why>`) / worker.
- Roll out `contract.go` in waves of disjoint packages. Highest seam value first: `llm`, `workflow`, `daemon`, `threads`, `mcp`, `integrations/*`, `temporal`.
- Split `internal/db`'s Repository into consumer-declared narrow interfaces (`api/role-interface` pattern) rather than one generated mega-mock.
- Replace hand fakes where a recorder mock is enough; keep stateful in-memory fakes.
- Each contract also gets the generated observability decorator, which is how per-component tracing and metrics arrive.

**P7 — Observability (M).** Depends on P3c.
- Adopt `observe` (logging/tracing/metrics interceptors, `LogCall/TraceCall/RecordCall`) and serverkit health/readiness/pprof/drain (`PreStopDelay` exists).
- Move probes from the separate `HEALTH_PORT` 8081 to serverkit's endpoints, **together with control-plane's `RELIANT_PROBES`** in `workloads.k:163`.
- Keep the daemon's rotating file logs (lumberjack, user machines) and Sentry + scrub as an adapter until F7 lands.
- Move the 1,290 `connect.NewError` sites to `svcerr.Wrap` package by package (lint `forgeconv-no-handler-error-mapping`).
- Turn on the LGTM stack + `forge env status` in dev.

**P8 — Dev tooling, CI, deploy (M).** Depends on P4 and P5.
- `forge env up dev` (control-plane) becomes the one dev path. Delete `scripts/dev.sh`, `dev.js`, `find-ports.js`, `.dev-ports.sh`, `dev-multi-instance.sh`, `list-instances.sh`, `logs.sh`, `docker-compose.yml`'s 5433 stack, `./data/logs`, and the air configs control-plane doesn't use. Update the `dev-logs` skill, CONTRIBUTING and `reliant.md`.
- Retire the Makefile for the Taskfile; keep the ~16 custom generators as Taskfile tasks under `generate`.
- CI: forge's `ci.yml` / `proto-breaking.yml` plus reliant's extras (release, license gate, pin-drift → forge pin check).
- D4 target once F6 is proven.

**P9 — Frontend (M, independent after P1).** Owns `web/`.
- Declare `frontends: [{name: web, type: vite-spa, path: web}]`. Adopt `@reliantlabs/forge-web-runtime` transport interceptors (auth, traceparent, error normalization, retry) and generated React Query hooks (gradually, per service).
- Move the 20 `VITE_*` vars to `config_gen.ts` / `window.__FORGE_CONFIG__`. Open question: how Electron serves `config.js`.
- **Styling:** reliant keeps its design (shadcn-style tokens, 7 schemes, `.dark`). Add a complete **token bridge** in `web/src/index.css` mapping forge's `ink/surface/border/accent/danger/success/warning` (+ `-surface/-border/-ink`) onto reliant's scheme variables. Today only `surface-sunken` is bridged, for the forge `card` already installed. After the bridge, every `forge component install` renders correctly in all schemes. Then replace `web/src/components/ui` primitives with forge components where they match, and upstream fixes (as was done for the card's elevation rule).
- Don't adopt forge's starter nav/dashboard/sign-in.
- Tests run under `task test:frontend`.

```
P0 ─► P1 ─┬─► P2 ─► P3a ─► P3b (×36 parallel) ─► P3c ─► P7
          ├─► P4 (parallel by package) ─────────────────┐
          ├─► P5a ─┬────────────────────────────────────┼─► P8
          │        └─► P5b (needs F5/F12, + P3b per svc) ─► P6 Repository split
          ├─► P6 (non-handler pkgs, parallel waves) ────┘
          └─► P9
```

## 5. Forge work items (fix in forge, not around it)

| # | Gap | Evidence | Needed by |
|---|---|---|---|
| F1 | No adoption path from an existing **goose** history (`goose_db_version`) to migratekit; backfill assumes `schema_migrations` already exists | migratekit API has `Open/Up/Force/State` only; reliant prod/desktop DBs are goose | P5 |
| F2 | Forge assumes every `contract.go` package is a **server** component; CLI-/daemon-only packages fail composition | `forge generate` #3: `Cliauth.Deps.Dirs … has no provider` | P1 (workaround `forge:external-component`) |
| F3 | Per-binary config (`forge.v1.binary_config {binary}`) is keyed on `cmd/<name>`; reliant's roles are **subcommands of one binary** (api/worker/gateway/daemon/CLI). Needs role/subcommand-scoped config, or forge has to bless splitting binaries | `proto/config/v1/config.proto` header comment | P4 |
| F4 | No **incremental service adoption**: the first `generate` scaffolds stubs for all 36 services / 307 rpcs at once, with no way to exclude a proto service until it's ported | §1 #4 output | P1/P3 |
| F5 | **CRUD auto-detection on name collisions**: 52 reliant rpcs match `Verb<E>` + table + message. Need an explicit per-rpc/per-entity opt-out, unless `//forge:no-orm` already suppresses it (to verify) | §2 | P5 |
| F6 | **Cross-project workload/config composition**: control-plane consuming reliant's `config_gen.k` + workload definitions from `../reliant` (KCL module dep) | D4 | P8 |
| F7 | No error-reporting (Sentry-style) hook in `observe`, and no rotating file-log sink for user-machine processes | `rg -il sentry forge/pkg` → test files only | P7 |
| F8 | `project new --in-place` writes the **running** binary's `forge_version`, not the version go.mod already pins | scratch `forge.yaml` | P1 |
| F9 | In-place adoption silently relies on the project's kept `buf.gen.yaml`; a plugin not installed (protoc-gen-es under `tools/`) fails generate instead of being detected in pre-flight | `forge generate` #2 | P1 |
| F10 | `forge project libraries` fails from `control-plane/` (`resolved to … which has no pkg/ directory`) | first probe of this session | — |
| F11 | **DONE**: forge PR #641 (`feat/remove-sqlc`), together with the `generate.hooks` escape hatch (reliant runs sqlc as a hook until P5b). **APPROVED 2026-10-10: remove sqlc from forge.** The forge ORM doesn't use sqlc: `forge/pkg/orm` is bun + pgx, the entity/store codegen is forge's own (`pkg/schemadef` → `internal/codegen`), and sqlc isn't in forge's `go.mod`. sqlc is only an optional bolt-on, and it's partly broken. Remove: (1) the `sqlc generate` pipeline step (`internal/cli/generate_pipeline.go:380`, `runSqlcGenerate` in `internal/cli/generate_tools.go:123`, `writesSqlc` in `internal/cli/generate_external_writes.go:98`); (2) the scaffold stub `generateSQLCStub` (`internal/generator/dx_files.go:1249`: `db/sqlc.yaml`, `db/queries/`, sqlc README). It's **dead on arrival**: generate only looks for a ROOT `sqlc.yaml`, so the scaffolded `db/sqlc.yaml` never runs; its paths are root-relative inside a file sqlc resolves relative to `db/`; and it targets `internal/db` package `db`, which collides with the generated ORM; (3) the apparently orphaned `internal/templates/project/sqlc.yaml`; (4) sqlc guidance in the `db`, `contracts`, `testing/integration` and `migration-service` skills and `README.md.tmpl`, replaced with the `_repo_ext.go` + `db.Bun()` complex-query path (CTEs, `SKIP LOCKED`, advisory locks, raw SQL) tested against pgtest; (5) the `doctor_tools.go:220` mention. Per forge's no-backcompat stance, an existing root `sqlc.yaml` just stops being run, and the CHANGELOG says so | `forge/internal/...` paths listed | P5b |
| F12 | ORM gaps the P5b spike surfaces (expected: no join helper per `db/crud-overrides`; possibly bulk upsert, array params, recursive CTE ergonomics, `NOTIFY`). control-plane opts out (`//forge:no-orm`), so reliant is the first large consumer | `control-plane/internal/db/doc.go:11` | P5b |

## 6. Open questions for you

Resolved 2026-10-10: D1–D4 accepted, with D3 revised so sqlc is removed long-term (P5b). Squashing to a baseline is taken as accepted under "otherwise sounds good".

Also resolved 2026-10-10: **F11 approved** (sqlc removed from forge itself, not just reliant), and the **migration squash is confirmed**.
3. **Binary shape**: keep one `reliant` binary (needs F3) or split server roles into their own binaries (`cmd/reliant-server`, keeping `cmd/reliant` for CLI + daemon), which fits forge's model today?
4. **Desktop/Electron**: is the desktop app's embedded Postgres/monolith mode in scope for P5's migrate-at-boot, and how should Electron deliver `config.js` (P9)?
5. **Forge-first ordering**: fix F1–F5 in forge before P1/P3/P5, or start P1 with the workarounds and fix in parallel?
