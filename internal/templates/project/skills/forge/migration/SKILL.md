---
name: migration
description: Migrate an existing project to forge — pre-flight, project shape, module path strategy, package porting order, common gotchas.
---

# Migrating an Existing Project to Forge

Use this skill when porting an existing Go codebase onto a forge-generated scaffold. For greenfield work see `forge`.

## Pre-flight

1. **No BSR (buf.build) auth required — Go or frontend.** Fresh forge projects scaffold with no `deps:` in `buf.yaml` and `local:` plugins on both halves of codegen:
   - **Go**: `local: protoc-gen-go` / `local: protoc-gen-connect-go` resolved from `$PATH`. `forge project new` auto-runs `forge tools install` to put them there.
   - **TypeScript** (frontends): `local: ./frontends/<name>/node_modules/.bin/protoc-gen-es` from `@bufbuild/protoc-gen-es` pinned in the frontend `package.json`. `forge project new` runs `npm install` in each frontend dir before bootstrap codegen.

   Opt back into BSR-hosted plugins with `forge project new --buf-plugins=remote` if you prefer no-install (and accept rate limits) on both halves.
2. Install dev tooling forge expects on PATH but does not ship: `buf` (no BSR auth needed), `goimports`, `npm` (only if you have frontends — and frontend TS codegen requires it), `go`. `forge project new` warns and continues without them but downstream `forge generate` and lint passes degrade silently.
3. Make sure your host Go version is `>=` the version forge's `pkg/orm` requires. Forge clamps `go.work` upward when it detects an older host, but a host mismatch can still surprise `go build` calls outside forge's own subprocesses.
4. PATH visibility in your shell does NOT propagate to forge subprocesses unless you `export` it. Add `export PATH=/path/to/go/bin:$PATH` to your shell init or invoke forge with the env inline. `buf generate` is run as a forge subprocess and will silently misbehave if it cannot find tools.
5. Load relevant skills upfront — they are the most current source of truth on conventions:
   ```bash
   forge skill load architecture
   forge skill load services
   forge skill load auth
   forge skill load contracts
   ```

## Choose the project shape at scaffold time

```bash
forge project new <name>-next --kind <service|cli|library> --mod <module-path>
```

| Kind | Use when |
|------|----------|
| `service` (default) | Network-facing app: Connect-RPC server, middleware stack, observability, k8s deploy. See `migration-service`. |
| `cli` | Cobra-based CLI binary. No `pkg/middleware/`, no `cmd/server.go`, no `deploy/`, no service protos. See `migration-cli`. |
| `library` | Pure Go module with an `internal/` skeleton (and `pkg/` only if you have real external importers); no `cmd/` at all. For shared libraries. |

**Pick this at scaffold time.** `--disable` flags only toggle `forge.yaml` features; they do NOT prevent server-shaped files from being emitted. If you scaffold with the wrong kind, wipe and start over rather than pruning by hand.

## Module path strategy

For a project nothing else imports, use a `-next` suffix (e.g. `github.com/owner/project-next`) during the migration so old and new repos build side-by-side, and rename the module path as the final cutover step. The friction is small and the safety upside is large.

A project other modules already `require` — or one too large and too busy to fork for months — adopts forge **in place** instead (`forge project new --in-place`), one service at a time. That only works because forge can be told which services it does not manage yet; see the next section.

## Adopting in place, one service at a time: `// forge:unmanaged`

The first `forge generate` on an existing tree discovers every proto service and, by default, scaffolds `internal/handlers/<svc>/` with an `Unimplemented` stub per rpc, mocks, a mount, a compose entry and a subcommand for each — beside the real handlers that already exist elsewhere. On a large API that is hundreds of stubs in one commit. Prevent it by marking every service whose handlers live outside forge:

```proto
// forge:unmanaged — handlers still in internal/grpc/services; adopt one per PR.
service ChatService {
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
}
```

- **What forge still does:** buf generates the service's Go/TS code as before, it stays in `gen/forge_descriptor.json`, and its rpcs' `auth_required` declarations still project into `pkg/middleware/procedures_gen.go` (whether an rpc is public is part of the contract, whoever serves it — so a hand-written server can use the generated open set).
- **What forge skips:** everything service-scoped — handler dir and rpc stubs, scaffold tests, mocks, CRUD wiring and entities, the mount, compose entry and subcommand, frontend hooks and pages. `forge scaffold` skips it and refuses `scaffold service|rpc|entity --from-proto` on it; `forge lint`'s list-filter rule and `forge project shapes` treat its rpcs as not forge's.
- **The ledger:** `forge project audit --json` lists every unmanaged service under the shape category's `unmanaged_services`; `generate` prints the count each run.
- **Adopt one:** delete its marker line, run `forge generate` — it scaffolds `internal/handlers/<svc>/` like any new service — move the real logic onto the generated `*Service` methods, swap the old mount for `Mount<Svc>`, and delete the old handler. One service per PR; the rest of the tree is untouched.
- **Hard to get wrong:** a near-miss spelling (`forge:unmanged`) or the marker on an rpc or message fails `forge generate` — it is a whole-service switch, never per-rpc.

A NEW service added to an in-place project gets no marker, so it is born forge-managed.

**`forge.proto` arrives with the first annotated proto.** An adopted project has no `proto/forge/v1/forge.proto` until it needs one: the first `forge generate` after a proto imports `forge/v1/forge.proto` (a typed config proto, say) vendors it. The project's own `buf.gen.yaml` must take it as an import only — add `inputs: [{directory: proto, exclude_paths: [proto/forge/v1, proto/buf/validate]}]` — or `forge generate` refuses it: its Go is `forge/pkg/forgepb`, and a second Go copy panics every binary at init. Exclude it at the INPUT; a plugin's `exclude_types` also keeps it out, but prunes the image buf hands every plugin, so it changes every other generated `.pb.go` too.

**Typed config starts advisory.** An in-place scaffold that finds an existing project writes `config: {enforce_typed_access: warn}` into `forge.yaml`, where a greenfield `forge project new` writes `error`. The project's existing `os.Getenv` reads are reported by `forge lint` instead of failing it. Move them onto the typed config (`proto/<project>/config/v1/config.proto`, loaded by `pkg/config`), then set `error`. `.golangci.yml` is yours, so re-render it after the flip (`rm .golangci.yml && forge project upgrade`).

## Service / package naming

- Hyphens are OK in service / worker / operator names (`forge scaffold service admin-server`). Forge stores the kebab-case form as the display name in `forge.yaml` and snake-cases the form used for the on-disk directory path (`internal/admin_server/`), with a single-token lowercase Go package decl (`package admin`).
- `forge.yaml` `path:` is always the snake form. Don't hand-edit it to use hyphens.
- The proto package generated for a pack like `stripe` is `db.v1` (not `<project>.db.v1`) — package names align with directory layout under the buf module root, not the project name.
- For the full kebab / snake / Pascal / camel mapping across Go, proto, TS, and `forge.yaml`, see the **Naming conventions** table in `architecture`.

## Recommended migration order

Same shape for both `service` and `cli` targets:

1. `forge project new <name>-next --kind <kind> --mod <path>` — scaffold the empty project.
2. Add components (`forge scaffold operator/service/worker/webhook`) one at a time. `forge generate` after each.
3. Get a green baseline before porting any business logic: `forge generate && go mod tidy && go build ./... && forge lint`. All four must pass.
4. **Set the contracts floor before porting.** Edit `forge.yaml`:
   ```yaml
   contracts:
     strict: true
     allow_exported_vars: false
     allow_exported_funcs: false
     exclude: []
   ```
   See the `contracts` skill. Enabling this upfront prevents a 5-hour backfill at the end.
5. Port app code into `internal/`. Everything not imported outside the module lives there — services (`internal/handlers/<svc>/`: `service.go`, the rpc methods and `handlers_crud_ops_gen.go` co-located in ONE dir, no `contract.go`; logic that needs isolation goes in a `forge scaffold package` domain package), workers (`internal/workers/<name>/`), operators (`internal/operators/<name>/`). Order: utility code and domain types first, then the service impls and handlers, then wiring. The wiring is the explicit composition (`internal/app/compose.go` `NewComponents(infra *Infra) (*Components, error)`), off the owned `internal/app/providers.go` `Infra`/`OpenInfra` provider set. Both files are yours: `providers.go` is scaffolded once, and `compose.go` is written once and then reconciled — newly discovered components and `Deps` keys are injected, your value expressions and construction order are kept (`forge project disown internal/app/compose.go` stops reconciliation). There is NO generated `bootstrap.go`/`wire_gen.go` re-emitted under you, so porting wiring is filling each component's `Deps` off `infra.<Field>` (resolved by type) in `NewComponents` and declaring any new collaborator on the `Infra` provider set. (`pkg/` is reserved for code you'll support as public API; absent real external importers, port into `internal/`, not `pkg/`.)
6. Add `forge lint --contract` to the gate after every port phase. If a port leaves a package without `contract.go`, lint fails — fix before moving on.

## Cross-cutting concerns (auth, audit, interceptors)

- Auth, audit, and API keys are code + `forge/pkg/*` libraries, not installable packs. Auth lives in owned `internal/app/auth.go` (`SetupAuth()`) over `forge/pkg/auth` + `forge/pkg/apikey`; audit is the `forge/pkg/audit` library; the frontend auth UI is owned scaffold under `frontends/<name>/src/`. See the `auth` and `frontend` skills. Third-party clients (Stripe, Twilio, NATS) are a thin wrapper you write under `internal/<name>/` — see the `adapter` skill.
- Build the interceptor chain with `serverkit.NewChain(serverkit.ChainDeps{…})` in the generated `cmd serve.go`, handing your `Auth` (required — `serverkit.AuthFor(audience, interceptor)`) / `Audit` / `RateLimit` interceptors in by named field; every other slot has a working default. Order is load-bearing and lives in `NewChain` (trace → recovery → request-id → logging → tracing → metrics, then deadline → auth → audit → rate-limit → extras → activity → stream-drain → protovalidate → svcerr) — it is never implied by registration order, so don't hand-roll a `connect.WithInterceptors(...)` sequence of your own. A second kind of caller (daemons, operators) gets its own chain and `serverkit.Listener` (`auth`, "Audiences").
- Add the host component before the code that extends it (a webhook receiver needs its service first), then `forge generate`.

## Per-package porting recipe (source-copy approach)

For each existing package you want under the new tree:

```bash
cp -r /path/to/source/internal/<pkg> /path/to/<name>-next/internal/
find /path/to/<name>-next/internal/<pkg> -name '*.go' \
  -exec sed -i 's|<old-module-path>|<new-module-path>|g' {} +
cd /path/to/<name>-next && go mod tidy && go build ./... && go test ./internal/<pkg>/...
```

For templates and embedded assets, restrict the rewrite to the runtime-import path (`pkg/...`) only. Doc-strings and `go install ...@version` references that point at the canonical tool MUST be left alone:

```bash
find /path/to/<name>-next/internal/<pkg> -type f \
  \( -name '*.tmpl' -o -name '*.go' -o -name '*.yaml' -o -name '*.json' \) \
  -exec sed -i 's|<old>/pkg|<new>/pkg|g' {} +
grep -rn '<old-module-path>[^-]' /path/to/<name>-next/internal/<pkg>
```

Triage every grep hit. README/`go install` references should remain canonical; runtime imports get rewritten.

## Common gotchas

- **Pre-port grep `^\s*"<old-module>/` in the source-side package** to enumerate every internal import path before copying. The per-internal counts tell you which other packages are needed and surface non-`internal/` deps (repo-root packages, `cli/` public-embed) that are easy to miss.
- **Pin transitive deps the source repo pins.** Before `go mod tidy`, copy version pins for any dep with a fast-moving API (Delve, gRPC, k8s libs, cobra) into the target `go.mod`. A blind `tidy` resolves to latest-compatible and silently breaks API skew.
- **`//go:embed` of in-repo assets.** Use `cp -r` (preserves dotfiles like `.dockerignore.tmpl`) and verify any string constants that match against embedded content still align after the import-path rewrite. Without that check, embed mismatches no-op silently and downstream scaffolds break at runtime.
- **Generated proto descriptors and sed do not mix.** A blanket `sed s|forge|forge-next|` rewrites the `go_package` string inside `*.pb.go` rawDesc bytes but does NOT update the varint length prefix → runtime panic in `protobuf/internal/filedesc.unmarshalSeedOptions`. Regenerate via `buf generate` instead of sed-rewriting compiled descriptors.
- **`.proto` files are a third file class** alongside `.go` and templates. Every proto declares `option go_package = "<module>/gen/<pkg>/v1;<pkg>v1"`, so the module-path rewrite must cover `proto/` too — then re-emit `gen/` with `forge generate` (never sed the compiled `*.pb.go`, per the gotcha above).

## Entity strategy in migrations: your schema is already the truth

When you migrate an existing project, you arrive with `db/migrations/`
already authoritative — and in forge, that IS the entity model. SQL is
the schema language: `forge generate` applies the migrations to an
in-memory shadow database, introspects the tables, and projects entity
structs, ORM, and CRUD wiring from them. There is no proto-side schema
declaration to keep in sync (the legacy `(forge.v1.entity)` annotations
are retired and ignored).

Copy the migrations over as-is, then decide per table:

- **Want generated CRUD?** Declare the five CRUD RPCs
  (`Create<X>`/`Get<X>`/`Update<X>`/`Delete<X>`/`List<Xs>`) in the
  service proto — the table plus the RPCs make an entity, and
  `forge generate` projects the ORM, conversions, and frontend pages.
- **Hand-rolled access?** Declare no CRUD rpcs. The table still gets its
  generated store (`<Entity>Store`, delegates, `_repo_ext.go`), and stays
  invisible to the CRUD/frontend projections. `COMMENT ON TABLE t IS
  'forge:no-entity'` keeps it off the store too.

One gotcha: the shadow apply (and your project's tests via
`pkg/testkit`) run the migrations on a real ephemeral postgres, so just
write plain postgres DDL — `DEFAULT (now())`
parenthesized, no `::type` casts. Postgres-only auxiliary DDL
(extensions, functions, triggers, comments) is skipped harmlessly; a
failing `CREATE/ALTER/DROP TABLE` is a hard generate error you fix in a
new migration. See the `db` skill for details.

If the source project was an older forge project with annotated entity
protos (or a `proto/db/` directory), delete both: the applied schema in
`db/migrations/` is the entity model now, and the annotations are ignored.

## Databases another migration tool already migrated (goose)

Converting the migration FILES is the easy half. The deployed databases matter
more: production, staging, a shared dev database, users' desktop installs.
goose has already migrated them, and the new binary must not run their history
again.

- **Databases you can rebuild:** `reliant forge project migrate import --from goose`
  converts and renumbers the files (`migration-service`, "Goose →
  golang-migrate").
- **Databases you must keep:** squash goose's history into ONE baseline as
  the first migration, and declare in its header the history it replaces:
  `-- forge:adopts goose <every goose version>`. `reliant forge db squash
  --from-dsn <dsn>` writes both from a throwaway database the whole goose
  history built (`goose up` on an empty database). The migrator adopts each goose
  database the first time it meets it, on the normal `db migrate up` /
  `AutoMigrate` path. It records the baseline as applied without running it
  when goose's applied set is exactly the listed one, and refuses with
  `*migratekit.AdoptionError` when the database is behind or holds a version
  the list does not account for. goose's table is never modified, so the last
  goose release stays a safe rollback target.

Ship the cutover as its own release, roll it out environment by environment,
and prove the baseline equals goose's schema with a `pg_dump --schema-only`
diff before the first deploy. `db/deploy-migrations`, "Adopting a database
another tool migrated", has the full rules, the command that generates the
version list, and the verification.

## When forge itself is wrong, mid-migration

Stop and report it, with a reproduction. Do not paper over it, and do not hand-edit generated output to compensate — `forge generate` will overwrite the patch and the defect returns. Fixing forge is a separate piece of work from your migration; detouring into it mid-migration means rebuilding a toolchain and re-verifying everything you had already checked against the old one.

## Tips for delegating port work to sub-agents

- Tell them which forge skills to load before starting (`architecture`, `services`, `contracts`, `migration` plus this skill's children).
- Tell them which fixes are already in (so they don't relitigate).
- Be explicit about whether to use `forge package new` or copy source directly.
- Specify env: `export PATH=/path/to/go/bin:$PATH`.
- Halt-and-report rule on new forge bugs (don't fix forge themselves — bring back to the orchestrator).

## Sibling skills

- `migration-service` — server-shaped projects (services, operators, workers, webhooks, packs, k8s)
- `migration-cli` — CLI / library projects (`cmd/`, second binaries, when contract.go isn't worth it)
- `migration-upgrade` — upgrading an *already-forge* project to a newer forge binary version

For contract design — applies during migrations and to greenfield code alike — see the top-level `contracts` skill.
