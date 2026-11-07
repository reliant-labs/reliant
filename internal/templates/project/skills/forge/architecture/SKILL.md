---
name: architecture
description: Architecture map — project structure, generated vs hand-written code, the generate pipeline, wiring, files-not-to-edit, and a naming + related-skills index. Deep topic detail lives in the owning skills (proto, db, contracts, service-layer, observability, testing, deploy).
---

# Forge Architecture & Conventions

Forge is a **production infrastructure generator**: middleware, mocks, observability, test harness, CI/CD, and wiring. You own all business logic and the database schema; forge never touches those.

**Two canonical inputs, two truths:**

- **Proto is the wire truth.** API contracts and config live in proto files annotated with `forge.v1` extensions (`method`, `service`, `config`). See `proto`.
- **SQL is the schema truth.** `db/migrations/*.up.sql` is the single source of truth for tables/columns. `forge generate` applies the migrations to an in-memory shadow database, introspects it, and projects entity structs, the ORM, CRUD wiring, and frontend pages. There is no entity annotation; columns come from the applied schema. See `db`.

An **entity** exists when both halves exist: a service proto declares the CRUD RPCs (`Create<X>`/`Get<X>`/`Update<X>`/`Delete<X>`/`List<Xs>`) AND the applied schema has the matching table (pluralized snake_case).

**The fast path (proto-first).** Mark each entity message with a leading `// forge:entity` comment, add custom RPCs, then run `forge scaffold`: it births every marked entity (CRUD quintet + owned create-table migration) and runs generate, stubbing every custom RPC (`--dry-run` plans; a clean re-run is a no-op). Depth lives in the owning skill — entity births/seeds in `db`, RPC handlers in `api`, domain packages in `contracts`/`service-layer`.

## Project Structure

**Principle:** express the app as explicit, owned, typed Go — not a string-keyed registry through a clever framework. The application lives under `internal/`, nested by role (`handlers`/`workers`/`operators`) so owned and generated files are **co-located** in one component directory. `pkg/` is the thin generated substrate the app imports — typed config, the middleware policy file, the test harness. Top-level is otherwise `cmd/` (entrypoints) and `api/` (genuinely-external CRD types).

```
cmd/                          # entrypoints ONLY: cobra root, one generated command per root (roots_gen.go), owned serve.go policy
api/v1alpha1/                 # CRD types — genuinely external (kubebuilder convention)
internal/                     # the application: components, DI, data access
  handlers/<svc>/             #   a service, all co-located in ONE dir:
    service.go                #     yours: Deps + validateDeps + New + Register
    handlers_crud.go          #     yours: thin CRUD delegations (access-control / row-scoping WHERE goes here)
    rpc_<name>.go             #     yours: ONE custom RPC per file (scaffold rpc + generate agree)
    handlers_crud_test.go     #     yours: CRUD lifecycle test (scaffolded once; rows from the factories)
    factories_gen_test.go     #     generated: New<Entity> / New<CreateRequest> test factories from the applied schema
    handlers_crud_ops_gen.go  #     generated: per-RPC CRUD ops + ToProto/FromProto
  handlers/mocks/             #   generated cross-service mocks (package mocks, one <svc>_mock_gen.go each)
  <name>/                     #   standalone domain package: contract.go (yours) + mock_gen.go
  workers/<name>/             #   workers (NOT top-level): worker.go + worker_test.go
  operators/<name>/           #   operators (NOT top-level): controller + types
  app/                        #   the composition graph — the LIVE composition root:
    providers.go              #     yours: provider funcs New<X>(<X>Deps) (T, error) — DB, ORM, auth, chain, listener, …
    auth.go                   #     yours: SetupAuth() over forge/pkg/auth
    compose.go                #     yours: one constructor method per node — reconciled: missing methods + Deps keys only
    roots.go                  #     yours (optional): //forge:root structs — what a process builds
    graph_gen.go              #     generated: memoized accessors g.<X>(), Build<Root>/Run<Root>, the default Server root
    routes_gen.go             #     generated: <Audience>Routes route sets, MountOptions, the data-only Inventory
  db/                         #   <entity>_orm.go (generated from schema) + <entity>_repo_ext.go (yours)
pkg/app/                      # substrate: testing.go (per-component test harness) + migrate.go
pkg/config/                   # typed config projected from the config proto (below)
pkg/middleware/               # thin owned policy file
gen/                          # ALL generated stubs/clients/mocks + forge_descriptor/ — never hand-edit
proto/services/<svc>/v1/      # protobuf service definitions (API contracts)
proto/<project>/config/v1/     # config truth: every field, its env var, flag, default
db/migrations/                # SQL migrations — THE schema source of truth   (db/queries/ — SQL queries)
frontends/<name>/             # Next.js frontends (src/app, src/hooks, src/lib)
deploy/kcl/<env>/             # KCL deployment manifests + per-env config       (e2e/ — end-to-end tests)
forge.yaml                    # project-GLOBAL config only: identity, frontends, packages, overrides
```

Config ownership follows the PROCESS: one project-global `AppConfig` by default, or a message per binary (`(forge.v1.binary_config) = {binary: "<cmd/name>"}`), or — for a binary whose subcommands are separate processes — per subcommand (`command: ["server", "api"]`), so each Deployment carries only its own process's env and credentials (`binaries` skill).

`pkg/app` is substrate only — the live DI composition is `internal/app` (`pkg/app/CONVENTIONS.md` documents the split). There is no top-level `handlers/`/`workers/`/`operators/` — that would falsely advertise public API. **What the project contains is discovered from the code** — service protos, `internal/workers/`, `internal/operators/`, `cmd/` — never from a list in `forge.yaml`.

## Generated vs Hand-Written

| Forge generates (safe to regenerate) | You own (Forge never touches) |
|---|---|
| `gen/` — Go stubs, TS clients, mocks | `internal/handlers/<svc>/service.go`, `rpc_<name>.go` — business logic |
| `internal/handlers/<svc>/handlers_crud_ops_gen.go` | `internal/handlers/<svc>/handlers_crud.go` — CRUD delegations (access-control WHERE) |
| `internal/app/graph_gen.go`, `routes_gen.go` (disown to hand-own) | `internal/app/providers.go`, `auth.go`, `roots.go` — providers, `SetupAuth`, roots; `compose.go` — yours, with only missing methods / Deps keys reconciled |
| `internal/db/<entity>_orm.go` — entity struct + ORM (from schema) | `db/migrations/` — schema truth; `internal/db/<entity>_repo_ext.go` — custom queries |
| `pkg/config/`, `pkg/app/testing.go`, frontend `*-hooks.ts`, `gen/forge_descriptor/` | `pkg/middleware/` — policy; `cmd/<binary>.go` — owned subcommands |

**Rule of thumb:** `_gen` in the name or under `gen/` — regenerated; `pkg/` is a mix (table above).

**Ownership signals.** Forge-owned files carry `// Code generated by forge. DO NOT EDIT.` + an embedded `// forge:hash=<sha256>` that travels through clones and worktrees, so the drift guard catches hand-edits anywhere — they abort regeneration until you move them to an extension point or take the one-way door (`forge project disown <path> --reason ...`). Yours carry `// yours: scaffolded once`; deleting one sticks (`.forge/scaffolded.json` records the birth), and a scaffold the project must never have — even one whose birth breaks the generate it is born in, which rolls it back with its record — is declined with `forge project disown <path> --absent --reason ...`, a decline a failed generate keeps (`forge project rescaffold <path>` re-adopts it). `forge lint --scaffolds` warns on surviving `// FORGE_SCAFFOLD:` TODO markers (not build-gating) and errors on a `_gen.go` missing its generated header.

## The Generate Pipeline

```
proto/services/<svc>/v1/<svc>.proto
  → protoc-gen-forge → gen/forge_descriptor/; protoc-gen-{go,connect-go} → gen/ stubs
  → forge generate → a pb-through stub per custom RPC (+ scaffold-once handlers_crud.go / handlers_crud_test.go)

db/migrations/*.up.sql (shadow-applied to a real ephemeral postgres, introspected) + CRUD RPC shapes
  → forge generate → internal/db/<entity>_orm.go, internal/handlers/<svc>/handlers_crud_ops_gen.go, frontend pages/nav/mocks

proto/<project>/config/v1/config.proto → forge generate → pkg/config/ + deploy/kcl/config_gen.k
internal/<name>/contract.go (standalone domain packages) → forge generate → internal/<name>/mock_gen.go
gen/ts/ → forge generate → frontends/<name>/src/hooks/*-hooks.ts
```

A generator forge does not own (a query generator kept during a port, your own codegen tool) runs as a **generate hook** declared in `forge.yaml`. Hooks run in order on the final tree, before `go mod tidy` and the validation build. A failing hook fails the generate and ends the report with its output tail (log: `.forge/generate-hooks/<name>.log`). Every full `forge generate` runs every hook; nothing skips them, because a skipped hook leaves stale output that still compiles. `FORGE_NO_GIT_HOOKS=1` (formerly `FORGE_NO_HOOKS`) is unrelated: it stops forge configuring git hooks in your clone. `writes:` puts the hook's output in generate's rollback journal:

```yaml
generate:
  hooks:
    - name: sqlc
      command: [sqlc, generate]          # argv, no shell; a shell line is [sh, -c, "..."]
      dir: .                             # optional, project-relative
      writes: [internal/db/postgres/generated]
```

`forge generate` is always safe — it only touches infrastructure, never your handlers, migrations, or business logic. In the DB layer it rewrites only `internal/db/<entity>_orm.go`. Observability is applied by `forge/pkg/observe` interceptors on the serve path, not per-package `_gen.go` wrappers — see `observability`.

## The composition graph (`internal/app`)

Every part of a process is a **component built from its Deps** — a handler, a package, a worker, an operator, and the things the owned `providers.go` declares: a DB pool, a client, an authenticator, an interceptor chain, a listener. A provider is any `func New<X>(<X>Deps) (T, error)` in `internal/app`; instance identity is the Go type (`type DaemonListener struct{ *serverkit.Listener }`), so **every Deps field resolves by type**, at generate time, never by name. The owned `compose.go` holds one constructor method per node, and its value expressions are the graph's edges:

```go
// internal/app/compose.go (yours; generate adds missing methods and Deps keys, never touches a value)
func (g *Graph) newBilling() (*billing.Service, error) {
    return billing.New(billing.Deps{
        Users:  g.Users(),          // another node, resolved BY TYPE
        Store:  g.LedgerRepo(),     // a provider func's value
        Config: g.Cfg,              // a graph input
    })
}
```

- **Accessors are memoized** (`graph_gen.go`): `g.Users()` constructs once. A **root** — a `//forge:root <name>` struct in `internal/app` — selects what a process builds: `Build<Root>` forces only the closure its fields reach, then seals the graph; `Run<Root>` supervises it (start in construction order, drain and close in reverse; field order is the stop order among siblings). With no declared root, forge generates `Server`: every runnable node nothing depends on — the API listener, the workers, the operators — which is what `<bin> server` runs.
- **The `Deps` interface is the seam.** A component depends on each dep's *interface*, so it cannot tell the in-process node from a Connect client or a mock. Splitting a service out later is a one-line change of the value expression.
- **Shape errors fail generate, named.** A required field nothing produces, a field two nodes could fill (ambiguous — write the key yourself in compose.go and forge keeps your choice), and a dependency cycle (break it with a `func() T` field: forge wires the accessor's method value, a lazy edge) each stop the generate with the node, field and type.
- **Route sets are per audience** (`routes_gen.go`): `UserRoutes` mounts every service whose proto declares the user audience (the default), `DaemonRoutes` every `audience: "daemon"` one; per-service mount options go in `compose.go` as `Options: MountOptions{…}`.

### Roots and commands

Every root is a command: `cmd/<bin>/cmd/roots_gen.go` (generated) adds one cobra command per root, named after the root or after its `command=` option — `//forge:root api command=server,api` runs as `<bin> server api`. With no declared root the one command is `server`. Each command hands its root to `runRoot` in the owned `cmd/<bin>/cmd/serve.go` — the one process policy every root runs under (config, logger, boot probes, migrate, telemetry, readiness gate, stop budget). Two facts come from the root's reach, read at generate time: a root that reaches the database migrates first, and a root with no API listener gets a probe-only listener on the server address, so every process answers `/healthz` and `/readyz`. While the graph is still being built, the boot probes' `/readyz` (503) names each node already built (`ok: true`) and each still under construction (`detail: "building"`), so a constructor that blocks boot is visible by its graph node name.

The root struct's doc comment is its command's help: the first paragraph is the Short, the paragraphs after it the Long (directives dropped), so describe the process there and keep notes for the code's readers on the fields or in a comment of their own. With no doc comment the help names the root's fields and start order. A binary whose root config is bound to it (`(forge.v1.binary_config) = {binary: "<bin>"}`) homes that config's flags on the binary's root command: when `root.go` calls `config.Register<Msg>Flags(root)` (persistent), the root commands inherit them and `roots_gen.go` does not register them again.

```go
// internal/app, conventionally roots.go — yours
//forge:root server
type Server struct {
    API *APIListener
}

//forge:root worker
type Worker struct {
    Tick *tick.Worker // `<bin> worker` builds the tick worker's closure alone
}
```

Declaring a root named `server` keeps the `server` command; once any root is declared, the generated default stops being generated. A project whose `serve.go` predates the per-root commands (no `runRoot`) keeps its own `server` command, and generate tells you to add `runRoot` before it writes `roots_gen.go`.

**Each root's config is its reach.** Generate reads, per root, the config fields its closure reads (`g.Cfg.X`, `g.Cfg.GetX()`, `g.Cfg.X.Y` in `compose.go`; `g.Cfg` handed on whole is every field), plus the process fields `runRoot` reads and, for a root reaching the database, the database fields. `runRoot` demands only those (`config.LoadReach`), so a worker root starts without `DATABASE_URL`. `deploy/kcl/config_gen.k` projects the same set as `ROOTS`: deploy a root with `fw.Workload {…} | config_gen.root_of("worker")` and `config_gen.workloadEnvMap(cfg, w)` gives it exactly that env — its required credentials, plus the optional ones its `config_secrets` opts into (a credential its root does not reach fails the render, as does an unknown root). The scaffolded `_api` is `root = "server"`. A component that hands on the whole config reaches all of it; a config block of its own is what narrows it.

**A binary that binds config per command** (`(forge.v1.binary_config)` `command` / `subtrees`) reaches across every config its root's process loads. Each of the primary binary's configs beside the root config is a graph input, `g.<Message>` (`*config.<Message>` in a Deps field resolves to it by type). A root at `server api` loads the root config, every subtree config over `server api`, and the `server api` command config; of each it reaches only what its closure reads through `g.<Message>`, with the same selector rules as `g.Cfg`. `roots_gen.go` loads each one with `config.Load<Message>Reach(cmd, reach)` and registers its flags where the binding puts them, so a required field only another root reads is never demanded. `ROOTS` projects the union through `<bin>ProcessEnvMap`, so `workloadEnvMap` takes the env's process bundle (`appcfg.<bin>_process`). Generate refuses a closure that reads a config its root's process does not load, because that field would be nil there.

**A config's invariants are its proto's `buf.validate` rules, checked as it loads.** A port's range, a positive duration, and a cross-field rule (`option (buf.validate.message).cel = {expression: "this.billing_provider != 'stripe' || this.stripe_secret_key != ''"}`) go on the config message, beside the fields. Every `config.Load*` runs them after `allowed_values` and the TLS/CORS role checks, so a refused value stops the process in its command's `RunE`, before `runRoot` binds a port or migrates; `config.Root.Validate` reports the same violations over a rendered environment at deploy time. Never check a setting in the constructor that reads it first: under a `migrate=apply` root that runs after the migrations. Presence stays `(forge.v1.config).required` (narrowed to the root's reach); a rule on a field that may be unset takes `ignore: IGNORE_IF_ZERO_VALUE`.

### Checking the whole graph, and what each node knows

`forge lint --graph` fails on a node no root reaches (resolution is scoped by reach, so a project declares its roots one at a time) and on a service whose audience its listener's authenticator does not serve. `forge lint --conventions` reads each component's `Deps` against what it calls (least knowledge), and `forge fix narrow-deps` narrows a field. A pre-graph project keeps `Infra` + `NewComponents` until `forge fix composition-graph`. The rules, severities and the migration path: load `architecture/graph-checks`.

## Files NOT to Edit

Regenerated (changes overwritten): `gen/`, every `*_gen.go`, `internal/db/<entity>_orm.go`, `pkg/config/`, `pkg/app/`, `frontends/<name>/src/hooks/*-hooks.ts` + `src/lib/connect.ts`, and the three generated `internal/app` files until disowned. Your CRUD bodies live in the owned `handlers_crud.go`; the validator behind `auth.go` is yours to pick — see `auth`.

## Naming conventions

Forge spans four ecosystems (Go, proto, TS, KCL) with different idiomatic casings; one identifier often appears in three forms. `forge scaffold` / `forge generate` translate, but write the right form by hand. **This table is the canonical reference — naming-adjacent skills (`services`, `migration`, `proto`, `proto-split`, `frontend`, `api`, `service-layer`) link back here.**

| Where | Form | Example |
|---|---|---|
| Component display name (KCL, `forge project map`) | kebab-case | `admin-server`, `git-credential` |
| Component directory on disk | role subtree + snake_case leaf | `internal/handlers/admin_server` |
| Go package declaration | snake_case (matches dir) | `package admin_server` |
| Go exported type / interface / method | PascalCase | `type Service interface`, `DoThing(...)` |
| Go local / variable / parameter | camelCase (initialisms stay caps) | `adminServer`, `orgID`, `createdAt` |
| Proto package | dot-separated lowercase, no project prefix | `services.users.v1` |
| Proto message / RPC / service | PascalCase | `User`, `CreateUserRequest`, `UserService` |
| Proto field | snake_case | `created_at`, `org_id`, `page_size` |
| Proto enum value | UPPER_SNAKE_CASE, enum-name prefixed | `TASK_STATUS_PENDING` |
| TS component file (`src/components/ui/`) | snake_case | `data_table.tsx`, `toast_notification.tsx` |
| TS hook / lib / store file | kebab-case | `use-api-query.ts`, `ui-store.ts` |
| TS component / type export | PascalCase | `DataTable`, `CardHeader` |
| TS hook / variable export | camelCase | `useListUsers`, `pageSize` |
| URL route param / query key | kebab-case | `/audit-events`, `?page-token=...` |

**Generated identifiers are spelled by their generator, not by this table.** Code that references generated Go or TS uses whatever protoc-gen-go, protoc-gen-connect-go or protoc-gen-es declared — and they disagree with a naive title-case at digits. protoc-gen-go capitalises the letter after a digit and keeps an underscore before one (`rpc GetOauth2token` → method `GetOauth2Token`, type `GetOauth2TokenRequest`; field `sha256sum` → `Sha256Sum`, `address_line_2` → `AddressLine_2`); protoc-gen-es does neither (`sha256sum`, `addressLine2`); connect-es lowers only an RPC's first letter (`LLMChat` → `client.lLMChat`). forge's own `db.<Entity>` rows keep forge's spelling (`Sha256sum`, `AddressLine2`). forge derives every reference it emits this way (`internal/naming/generated.go`); when you name a message, RPC or service, prefer a spelling already in the generators' casing (`Oauth2Token`, not `Oauth2token`) so the proto, Go and TS names read the same.

Lint enforces the structural halves (`forgeconv-one-service-per-file`, `forgeconv-internal-package-contract-names`, `--scaffolds`); `gofmt`/`goimports`/`staticcheck` enforce the Go-style rules.

## Deep detail lives in the owning skill

For the annotation vocabulary — every `// forge:*` marker, the proto→column mapping a birth applies, the projected `buf.validate` rules, and the `(forge.v1.service)`/`(forge.v1.method)` options — run **`forge project annotations --json`**: emitted from forge's own descriptors, so it cannot drift.

This skill is the map. For depth, load: **proto** (annotations, CRUD naming, field rules); **db** (migrations, entity types, schema evolution, ORM, op-overrides); **contracts** (contracts at every boundary, `contract.go`, strict mode); **services** (`<Component>Config` config blocks → `pkg/config` + per-env KCL); **service-layer** / **api** (interfaces-at-the-consumer, structs-out, role interfaces); **observability** (interceptor chain, per-method decorator, dashboards); **testing** (`pkg/app/testing.go`, real-DB integration, mocks); **deploy** (KCL normalize-for-the-author ⇄ denormalize-for-the-machine, `forge.render`).

## Rules

- Never hand-edit anything under `gen/` or any `*_gen.go`. Fix the proto or contract, then regenerate.
- The app lives under `internal/`, by role; `pkg/` is generated substrate (config, middleware, test harness); top-level is otherwise `cmd/` + `api/`.
- Wiring is the composition graph: provider funcs in `providers.go`, one constructor per node in `compose.go`, roots that select a process's subgraph — `Deps` resolve by type, never by name.
- `forge generate` never touches `providers.go`/`auth.go`/`roots.go`/`cmd/<binary>.go`, and in `compose.go` it reconciles only the projection — missing constructor methods and Deps keys — never your value expressions (`forge project disown` to stop even that).
- One service per proto package; one `internal/handlers/<svc>/` per service. Field numbers are forever — mark removed fields `reserved`.
- DB schema evolves via migrations, not proto; wire messages evolve independently and conversions map the intersection by name. An entity needs both halves.

## Related skills

Also: forge, frontend (state, patterns), frontend-testing, auth, workers, debug.
