# Reliant → Forge migration: shared research brief

This is the shared context for the research fan-out that plans migrating the
`reliant` repo onto forge. Every area researcher reads this first, then writes
ONE findings file next to it (`NN-<area>.md`). The orchestrator synthesizes
those into `PLAN.md` in this directory.

**These files are durable — never delete any file in this directory.**

## Goal (user's words)

> "let's plan out migrating reliant to forge" — "migrating to the contract code,
> config system, scaffolded mocks, dev tooling, observability, even frontend
> styling, etc"

The goal is broad adoption of forge, not a narrow port. The deliverable at this
stage is a PLAN, not code.

## The three repos (all checked out side-by-side in this worktree root)

Root: `/home/workspace/.reliant/worktrees/reliant-labs/migrate-reliant-to-forge-758c83c8/`

| Dir | Module | Role |
|---|---|---|
| `reliant/` | `github.com/reliant-labs/reliant` | THE PRODUCT. Branch `migrate-reliant-to-forge`. NOT yet a forge project (no `forge.yaml`). |
| `control-plane/` | `github.com/reliant-labs/control-plane` | Already a forge project (`forge.yaml`, `harness: reliant`). Billing, LLM gateway, daemon lifecycle, workspace operator, admin-web. **Runs reliant's binaries in its dev stack and declares reliant's k8s workloads.** |
| `forge/` | `github.com/reliant-labs/forge` | The framework/generator. `forge/pkg/*` = public Go libraries; `web-runtime/` = `@reliantlabs/forge-web-runtime`. |

## Settled facts (verified by the orchestrator — do NOT re-derive)

### reliant, today
- ~320K lines of non-test Go, ~2,500 `.go` files, **104 packages under `internal/`**, `pkg/llmcatalog` the only `pkg/`.
- **One binary**: `cmd/reliant` (cobra, `cmd/reliant/commands/*.go`, 46 files). Server roles are subcommands: `reliant server api`, `reliant server worker` (Temporal worker), `reliant server gateway` (daemon gateway). The daemon (`reliant daemon ...`, runs on the USER's machine, talks over NATS) and the user-facing CLI (`auth`, `project`, `workflow`, `trigger`, `db migrate`, `open`, `forge-credential`, ...) are the same binary. Also `cmd/check-migrations`, `cmd/protocheck`.
- Wiring packages today: `internal/serverapi`, `internal/serverworker`, `internal/servergateway`, `internal/daemon`, `internal/grpc`, `internal/workersetup`.
- **Proto**: 38 files, all in ONE package `proto/reliant/v1/` (`reliant.v1`), **307 rpcs**, ~34 services. Every file has 1 service except `workflow.proto` (2). Go gen → `gen/reliant/...`; TS gen → `web/src/gen` (protoc-gen-es, `target=ts`). All buf plugins are `local:` and pinned (`buf.gen.yaml`). A second template `buf.gen.controlplane.yaml` copies control-plane protos into `gen/controlplane` (reliant must never import control-plane's module).
- **Codegen**: `Makefile` (no Taskfile). `make generate` runs ~16 custom generators beyond proto/sqlc: yaml-bindings, tool-catalog, schema, scenario-schema, refcheck, cel-reference, cli docs, tools-ref, shortcuts, nodes, types, models, presets, workflow-builder-skill, changelog, mintlify-reference.
- **DB**: Postgres only. **goose** migrations in `internal/db/migrations/postgres/` (146 files, already UTC-timestamp versioned, e.g. `20261007151318_add_daemon_name.sql`, no down migrations — `no_down_migrations_test.go`). **sqlc** (`sqlc.yaml`; 33 query files in `internal/db/postgres/queries`, `schema.sql`, generated package `pgdb` in `internal/db/postgres/generated`, `emit_interface: true`). Hand-written store/repository layer on top (`internal/db/*.go`, `internal/db/postgres/*_store.go`, `internal/db/core/` domain types). `internal/db/migrationcheck`, `schema_drift_test.go`, `cmd/check-migrations`.
- **Contracts**: 12 `contract.go` files already exist (`accesstokenclient`, `forgecred`, `integrations/{ghaccess,ghusers,webhook}`, `launch`, `connectorgrant`, `runs`, `gitcredentialclient`, `cliauth`, `tokenauthority`, `connections`). ~292 `type X interface` declarations in `internal/`. Hand-written fakes are rare (~8 `*fake*`/`*mock*` files). No mockery/gomock.
- **reliant already imports forge**: `go.mod` requires `github.com/reliant-labs/forge v0.1.44-0.20261007193810-e75c74afb46e` (same pin as control-plane). Used by `cliauth`, `tokenauthority`, `connections`, `forgecred`, `mcpserver`, `cmd/reliant/commands/forge*.go`, etc.
- **Config**: `internal/config/` (`config.go`, `loader.go`, `provider.go`, `project_meta.go`, ...); `config/reliant-schema.json` (the PRODUCT's user-facing config schema — distinct from process/server config); `config/shortcuts.yaml`.
- **Observability**: `internal/logging` (file handler + rotation, sentry handler, metrics handler), `internal/observability` (metrics, tracing, nats, temporal, label_safety), `internal/telemetry` (sentry, scrub). Separate health server on `HEALTH_PORT` (default 8081) and pprof on `PPROF_PORT` (6060).
- **Infra deps**: Temporal, NATS, Postgres, Sentry, OTel, Supabase auth.
- **Dev tooling**: `scripts/dev.sh` (+ `dev.js`, `dev-electron*.{js,sh}`, `dev-multi-instance.sh`, `find-ports.js`, `.dev-ports.sh`), `.air.toml`/`.air.api.toml`/`.air.worker.toml`, `docker-compose.yml` (Postgres on **5433**), logs to `./data/logs*` in that mode. `Makefile` test targets (`make test-short PKGS=...`). CI in `.github/workflows/` (`pr-ci`, `build-images`, `release`, `check-migrations`, `pin-drift`, `license-gate`, `gate-health`, `release-tag-guard`).
- **Frontend**: `web/` is a **Vite 7 + React 19 + Tailwind 4 + TanStack Router + React Query + Connect-ES** SPA. `web/src/{components,hooks,store,themes,styles,lib,...}`; `web/src/components/` contains `forge-ui/` and `Forge/` dirs. Custom Vite plugins: `vite-plugin-browser-logs.ts` (mirrors console to logs), `vite-plugin-csp.ts`, `vite-plugin-version-json.ts`. Vitest + Playwright. `electron/` desktop app wraps the web build. Repo-local skills in `reliant/.reliant/skills/`: `dev-logs`, `oauth-provider-login`, `onboarding-flow`, `web-styling`.
- Repo rules (from reliant's memory): **pre-launch, never require backwards compatibility; remove old code paths**. Reliant is a multi-tenant distributed system (api, worker, gateway, plus the daemon); API server / worker / gateway never touch the user's filesystem — only the daemon does.

### control-plane ↔ reliant coupling
- control-plane `go.mod` **requires `github.com/reliant-labs/reliant`** (so whatever reliant packages it imports must stay non-`internal` and stable through the migration).
- control-plane's `forge env up dev` (`deploy/kcl/dev/main.k`) launches reliant-api-server and reliant-temporal-worker from `../reliant` as host processes (`forge.HostDeploy {command, working_dir}`, air configs), the reliant Vite dev server, opt-in Electron (`-D electron=1`), and mirrors ALL logs into `control-plane/.forge/logs/dev/`. It allocates reliant's ports (`_reliant_grpc_port = fp.allocate_port(3091, key)`, health 8081, pprof 6060, web 3000) and its DB name (`identity.reliant_db_name`). Its Postgres is 5434 (DB `reliant` = real data: READ-ONLY, never mutate).
- control-plane's `deploy/kcl/workloads.k` declares reliant's cluster workloads (`reliant_api_server = fw.Workload{...}` ~line 611, `RELIANT_PROBES`, `reliant_db_provision`, ...). `desktop_release.k` handles desktop releases.

### forge, today (what it offers)
- `forge/pkg/*` libraries: `accesstoken apikey appkit audit auth authn cloudcred cmdkit components config contractkit controller credentials crud crypto deploy deploystate devidp diagnostics forgepb kcleval lifecyclekit middleware migratekit money mountkit oauth2 observe orm pgtest release schemadef seedplan serverkit svcerr tdd testkit validate`.
- Frontend: `forge scaffold frontend <name> --kind vite-spa` exists (besides Next.js default and `mobile`). `@reliantlabs/forge-web-runtime` (transport interceptors: auth/brand/traceparent/error normalization/retry; session/error-boundary/toast providers; `<Resource>`; client telemetry). Component library of 74 components on theme tokens (`ink`, `surface`, `border`, `accent`, `danger`, `success`, `warning` + `-surface/-border/-ink` variants). Generated React Query hooks per service.
- `forge project new --in-place [--name reliant] --mod ...` exists and **never overwrites an existing file** (keeps yours, lists what it kept; `--force` replaces). Inside an existing git repo it does no git init/commit. The `migration` skill instead recommends a side-by-side `<name>-next` repo — for a 320K-LOC product under heavy parallel development that recommendation needs to be challenged (orchestrator decision D1 below).
- Relevant forge skills (load with `reliant forge skill load <path>`): `forge`, `architecture`, `migration`, `migration-service`, `migration-cli`, `contracts`, `services`, `service-layer`, `interactor`, `adapter`, `api`, `auth`, `db` (+`db/*`), `proto`, `proto-split`, `observability`, `secrets`, `testing` (+`testing/*`), `dev`, `deploy` (+18 subs), `ci`, `frontend` (+10 subs incl. `frontend/design`, `frontend-runtime`, `frontend-testing`), `workers`, `binaries`, `forge-libraries`. `reliant forge skill list --all` for the full table.
- Introspection: `reliant forge project capabilities`, `reliant forge project annotations`, `reliant forge lint --help`, `reliant forge scaffold --help`.

## Orchestrator-level decisions (give evidence + a recommendation; do NOT decide unilaterally)

- **D1 — In-place vs `-next`.** Adopt forge inside the existing `reliant` repo incrementally (`forge project new --in-place`, then convert subsystem by subsystem), or the migration skill's side-by-side `reliant-next` repo?
- **D2 — Proto layout.** Keep `proto/reliant/v1/*.proto` (`reliant.v1`, procedure names `/reliant.v1.XService/...`) or move to forge's `proto/services/<svc>/v1/<svc>.proto`? Wire-name changes ripple to web, Electron, CLI, daemon, and control-plane's `reliant.v1.*` proxy.
- **D3 — DB layer.** Keep sqlc + the hand store layer (`//forge:no-orm`) and adopt only forge migrations/migratekit/testkit/lints, or move to forge's ORM/CRUD projection?
- **D4 — Where reliant's dev/deploy truth lives.** reliant gets its own `deploy/kcl/<env>` and `forge env up`, or control-plane keeps owning reliant's workloads and host processes?

## Rules for every researcher

1. **Read-only on all three repos.** The only file you write is your own findings file in this directory. Other agents are working in these checkouts in parallel.
2. **Experiments go in a scratch copy, never in the tree.** E.g. `s=$(mktemp -d) && git -C reliant archive HEAD | tar -x -C "$s"`, then run `reliant forge project new --in-place --name reliant --mod github.com/reliant-labs/reliant` / `reliant forge lint ...` there. Concrete evidence from a real run beats reasoning about forge's source.
3. **Never kill processes** (`pkill`/`killall`/`kill`): you are hosted by forge/reliant processes. Never touch Postgres 5434 (real data). Don't run `make test`/`make test-all` in reliant (they stop other agents' stacks).
4. Use `reliant forge ...` for forge commands. The installed forge binary (`v0.1.44-0.20261010080729-689f9e10cbeb`) is slightly newer than the pin; that's fine for research. **Run `reliant forge project libraries <pkg>` from the `forge/` directory** (from `control-plane/` it currently fails to resolve the module — known; do not chase it).
5. Prefer `code_context` for Go/TS call graphs, `rg` for everything else. Cite evidence as `repo/path:line`.
6. When forge is missing something reliant needs, that is a **forge work item**, not a reason to keep reliant's bespoke version forever. List forge gaps separately, with a concrete repro or the exact missing capability.

## Findings file template (use these headings)

```
# NN — <Area>
## TL;DR (5–10 bullets)
## Current state in reliant (with file:line evidence, sizes/counts)
## What forge offers (skills/libraries/commands, with what you verified by running it)
## Gap analysis (table: concern | reliant today | forge today | action: adopt / adapt / extend-forge / keep)
## Recommended target design (+ realistic alternatives, trade-offs, and a devil's-advocate pass on your own recommendation)
## Migration steps (ordered; each sized S/M/L; mark which can run in parallel and their disjoint file ownership; each step leaves the build green)
## Forge work items (gaps/bugs to fix IN forge, with repro)
## Dependencies on other areas (by file name, e.g. "needs 01-project-shape D2 resolved")
## Risks
## Open questions for the user
## Input on D1–D4 (only where your area has evidence)
```

## Area files

| File | Area |
|---|---|
| `01-project-shape-codegen.md` | forge.yaml adoption, cmd/ + binaries, composition root, proto layout, codegen pipeline, Makefile→Taskfile, CI, pinning |
| `02-contracts-mocks-testing.md` | contract.go per package, generated mocks, package boundaries, testkit/tdd, test lanes |
| `03-config-secrets.md` | process config → forge typed config, secrets provider model, env-var census |
| `04-db.md` | goose/sqlc/store layer vs forge migrations/orm/migratekit/testkit/seeding/lints |
| `05-server-runtime-observability.md` | logging/metrics/tracing/sentry, observe interceptor chain, serverkit, svcerr, auth/authn, validate, workers (Temporal), daemon/NATS |
| `06-dev-tooling-deploy.md` | dev.sh/air/compose/Electron dev vs `forge env up`, deploy KCL ownership, images/release, control-plane coupling |
| `07-frontend.md` | Vite SPA vs forge vite-spa, forge-web-runtime, generated hooks, component library + theme tokens vs reliant's styling contract, browser logs, testing |
