---
name: forge
description: Start here for any project — static site, SPA, landing page, docs site, web/mobile app or API. The two paths (frontend-only sites, and apps with an API), the entity-authoring rules, what scaffold births, the four commands that print forge's whole surface, and the pre-flight checklist that stops you hand-writing what forge already scaffolds.
---

# Forge — start here

Self-sufficient for greenfield work: empty directory to a running app without
loading another skill. Depth is named at the point you would need it — load it
then, not before.

pnpm: `migrations/v1.0.0/pnpm`.

## Two paths, both first-class

**A frontend with no backend of its own** — a landing page, marketing site,
docs site, SPA or PWA — is a **static site**, and it is a complete forge
project, not a stripped-down one. It gets dev/staging/prod envs from day 0,
`forge env up dev` with no database or cluster, build-once releases promoted by
digest, hosted static hosting (the platform owns the bucket, the CDN and the hostname;
one static site per org is free, and each env's site counts), custom
domains, per-env runtime
config without a rebuild, and the component library. Adopt an existing Vite or
Next.js app in place with `forge project new --in-place --kind site`. Either
way, **load `deploy/static-site` now** — it is the whole path, and the service
sequence below does not apply. (For a PWA, forge builds, hosts and ships it;
the service worker and manifest are yours to add.)

**An app with an API** — services, a database, a frontend over generated
Connect RPC hooks — follows the sequence below.

## Greenfield, end to end

```bash
forge project new my-app --mod github.com/acme/my-app \
  --service customers --service jobs --service estimates --frontend web
cd my-app
# author each service proto: entity messages + custom rpcs (rules below)
forge scaffold --dry-run     # print the plan
forge scaffold               # birth entities, then generate
forge lint --quiet           # only failures, then the verdict as the last line
go build ./...
forge env up dev             # host services + frontends, on a fresh seeded DB
```

### Name every service up front

`forge project new --service a --service b --frontend web` composes every
service into one graph; `<bin> server`, its default root, serves them all on
one mux. `main.go`'s `cmd.Execute(...)` builds the command tree, with no
command per service: every env runs `server` as `_api`. A later
`forge scaffold service <name>` joins the same way.

Batch later ones: `forge scaffold service sales jobs billing` generates once.

A bare `forge project new` scaffolds **zero services**: binary shell, `internal/app`
composition root, buf/proto scaffolding, Taskfile/CI/deploy. The binary is a
deployment unit, not a domain entity, so forge never invents a service from its name.

### The `project new` flags that matter

| Flag | Purpose |
|---|---|
| `--mod` | Go module path (`github.com/acme/my-app`). Defaults to the project name; set it to the path you will publish under |
| `--service <name>` | repeatable or comma-separated. Name after a domain entity, never after the binary |
| `--frontend <name>` | repeatable or comma-separated Next.js frontends |
| `--in-place` | scaffold into the current directory; takes no positional name |
| `--kind site` | `--in-place` only: adopts the Vite or Next.js app at the directory root as a frontend-only project — writes `forge.yaml` and the `deploy/kcl` envs, no Go (`deploy/static-site`) |
| `--name` | names an `--in-place` project whose directory is a worktree or branch name rather than the product |
| `--path <dir>` | parent directory for the project (default `.`) |

Everything else has a working default — `--kind` (`service` / `cli` / `library`,
each with its own `features:` block), `--harness`, `--disable`, `--go-version`,
`--force`: `forge project new --help`.

## Authoring a service proto

Three rules cover what agents get wrong.

### 1. Mark entities, and let scaffold inject their CRUD

Give the message a leading `// forge:entity` comment. Bare `forge scaffold`
injects the missing CRUD quintet (Create/Get/Update/Delete/List) into the
service proto, writes the create-table migration pair, and declares the managed
`id` / `created_at` / `updated_at` fields.

```protobuf
// forge:entity
message Customer {
  string name = 2;
  string email = 3 [(buf.validate.field).string.email = true];
}
```

There is no CLI field grammar — enums, `optional` (nullable), `buf.validate`
rules, `forge:read-only` / `forge:secret` / `forge:append-only`, a same-row
derived column (`forge:generated <sql expr>`) and foreign-key references are
all spelled in the message. Full vocabulary: `forge project annotations`.
Which marker keeps clients off a column your code owns (`forge:read-only`,
`forge:computed`, `forge:generated`, `forge:guards`, `forge:immutable`): the
decision table in `db/write-policy`.

`protovalidate` vendors itself: add `import "buf/validate/validate.proto";` and
`forge generate` pulls the protos into your `proto/` tree on the first run that
imports them. Fixed-length codes take an exact rule — `string.len = N` (ISO
currency 3, country 2) or `string.const` — because `buf lint` rejects
`min_len == max_len`.

### 2. Every custom rpc is yours to declare

**Forge injects CRUD rpcs only.** Authoring a `FooRequest`/`FooResponse` message
pair does not create an rpc — nothing scans for orphan message pairs. If the
`service {}` block does not name it, the rpc does not exist, and you find out at
`go build`.

Declare it in the block yourself:

```protobuf
service EstimateService {
  // CRUD quintet injected here by `forge scaffold`
  rpc RecalculateEstimate(RecalculateEstimateRequest) returns (RecalculateEstimateResponse);
}
```

Then `forge scaffold` emits the pb-through stub (a method on `*Service`
returning Unimplemented) in `internal/handlers/<svc>/rpc_<name>.go`.
`forge scaffold rpc <svc> <Name>` does the same for one rpc, and prints a proto
snippet to paste when the rpc is not in the proto yet.

**What makes an rpc CRUD is its exact name.** A unary rpc named `Create<E>`,
`Get<E>`, `Update<E>`, `Delete<E>` or `List<Es>`, where `<E>` has a table AND a
proto message of that name, gets the CRUD wiring. Anything else is custom:
`UpdateJobStatus`, `CreateInvoiceFromEstimate` and `ListJobsByCrew` get a stub
and no page, and a custom mutation's hook invalidates every query on its
service (a CRUD mutation's, only its entity's). Name rpcs for the domain; never
rename one to dodge CRUD detection. An rpc that is CRUD in name only says so:

```protobuf
// forge:custom — creating a task also wires its dependencies.
rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);
```

It is then custom everywhere (stub, test row, no shim, no page). Mark every
CRUD-named rpc of a table and the table gets no CRUD wiring, page or seed root.
It keeps its store: the ORM projects every applied table, rpcs or not; keep a
table off it with `COMMENT ON TABLE t IS 'forge:no-entity'`.

Every rpc is closed to unauthenticated callers unless it carries
`auth_required: false` (`proto`, `auth`).

### 3. Declare the authoritative route when two FKs reach one parent

When a table reaches the same parent two ways — `estimates.customer_id` directly
**and** `estimates.job_id → jobs.customer_id` — both foreign keys are valid and
the two routes can name different rows. Nothing in the schema says which wins,
so `forge db seed` refuses to write those rows until you declare it.

This shapes your entity model, so decide it while authoring the proto, not when
the seeder complains. Declare it as a comment on the FK constraint, in a
migration:

```sql
COMMENT ON CONSTRAINT estimates_customer_id_fkey ON estimates IS
    'forge:ref derived-from=job_id';
```

| declaration | meaning |
|---|---|
| `forge:ref derived-from=<column>` | the route leaving by `<column>` decides; this column is seeded from it |
| `forge:ref authoritative` | this column is the truth; the other edge is narrowed to agree |
| `forge:ref independent` | genuinely unrelated facts |

`authoritative` is usually right: the record states its own subject and the
longer route is a back-reference. Depth, and the cases forge resolves silently:
`db/seeding`.

## The shape, in four lines

- **SQL is the schema truth.** `db/migrations/*.up.sql` is the single source; entity structs, the ORM, CRUD wiring and frontend pages are PROJECTIONS of the applied schema. Do not declare schema in proto.
- **Proto is the wire truth.** Services, rpcs, messages. The two evolve on independent clocks; generated conversions map their intersection by name.
- **An entity is both halves**: CRUD rpcs in the proto AND the matching table. One half alone generates honest nothing.
- **Draft, then `forge generate`.** It is a fast, exact oracle that fails loudly. Never reverse-engineer forge's `internal/**` for syntax.

The application lives under `internal/`, nested by role: `internal/handlers/<svc>/`
(one service — owned and generated files in one directory), `internal/workers/<name>/`,
`internal/operators/<name>/`, `internal/app/` (owned `providers.go` + owned,
reconciled `compose.go`), `internal/db/`. `pkg/` is the generated substrate the app imports;
`cmd/` is entrypoints only. Full tree and the composition root: `architecture`.

### The scaffolded frontend is starter code

The dashboard grid, sidebar nav, sign-in screen and generated CRUD pages exist
to prove the stack is wired — Rails scaffolding, in React. They are all yours to
rewrite, and rewriting them is expected. Forge withholds a visual identity
deliberately: an invented one is harder to replace than an obviously neutral
one. Before showing any of it to a user, load `frontend/design` — it asks for a
brief first and will not invent an aesthetic without one.

A Next.js frontend builds as a **static export** (`output: static`, into
`out/`), the only shape hosted static hosting serves. So every route is
static: detail and edit are `/<entity>/view?id=…` and `/<entity>/edit?id=…`
(`src/lib/entity-routes.ts`). A hand-written `[id]` segment fails
`npm run build`. Depth: `frontend/serving`. If the project's pages predate
v0.1.44 and still use `[id]`, load `migrations/v0.1.44`.

## Evolving the schema

Migrations lead; projections follow.

```bash
forge db migration new add_login_tracking    # creates the .up.sql only — migrations roll forward, there is no down
# write plain postgres DDL — postgres-pinned, so ::casts, JSONB and TEXT[] all work
forge generate                               # re-project the entity struct/ORM
forge db migrate up --dsn "$DATABASE_URL"    # apply against a live database
```

Generated conversions map the intersection of wire fields and columns by name: a
DB-only column never leaks onto the wire, a wire-only field never reaches the DB.
Add either side freely. Forge stores no ownership of its own — if rows belong to
someone, that is a column you write in the migration, and adding it later costs a
backfill. Depth: `db`.

## Implementing business logic

Write it in `internal/handlers/<svc>/`, on the `*Service` methods. A custom rpc is
a method on `*Service` working with the wire (pb) types and the `internal/db/` ORM
through `s.deps`. CRUD rpcs delegate to `pkg/crud` via the owned shims in
`handlers_crud.go`. There is no `contract.go`/interface split inside a handler
package — the `*Service` methods **are** the handlers. When an rpc's logic
deserves transport-free tests or cross-service reuse, scaffold a standalone
package (`forge scaffold package <name>`) and call it via `s.deps`. Depth: `api`,
`service-layer`, `architecture`.

## Four commands print forge's whole surface

Read these before deciding forge cannot do something. All four are enumerated
from forge's own command tree, recognizers, descriptors and package set, so they
cannot drift the way a document can.

| Command | Answers |
|---|---|
| `forge project capabilities` | **Every verb forge has**, with a one-line summary — plus every `forge lint` analyzer (including the ones hidden from `--help`) and every `// forge:*` marker. |
| `forge project annotations` | The entity-authoring vocabulary: how each proto field is born as a column, the `buf.validate` rules forge projects, and the `(forge.v1.service)` / `(forge.v1.method)` options. |
| `forge project libraries` | **Every `forge/pkg/*` library**, what each is for, and the absolute path to the source this project resolves. |
| `forge project audit --json` | What THIS project currently is: services, rpcs, entities, drift, stubs, ownership. |
| `forge project shapes` | **Where every symbol IS**: each rpc, message, enum, table, handler and hook with `file:line`. Start recon here. |

Add `--json` to any of them to query rather than read.

Start recon with `forge project shapes --grep <Name>`: one symbol across proto,
schema, handlers, hooks and stores with `file:line`. Use `--unimplemented` for
missing handlers and `--service <Name>` to narrow it. Full recipes:
`forge skill load shapes`.

For forge's OWN APIs, use `forge project libraries <pkg>` — every signature,
without hunting generator source. For your generated stores,
`go doc ./internal/db <Entity>Store` renders the full interface.

## Running it

| Command | What it does |
|---|---|
| `forge env up` | Host services + frontends; auto-seeds a fresh dev DB on first boot |
| `forge env up dev` | Full stack: host infra (postgres, dev IdP) + the API (air, hot reload) + frontends |
| `forge env up <env>` | Build + deploy + host launch + frontend dev — reads `deploy/kcl/<env>/` |
| `forge env deploy dev` | Deploy to local k3d (or whatever dev's KCL targets) |
| `forge generate` | Re-project from protos + applied migrations. Safe anytime; never touches business logic |
| `forge lint` | Go + proto + frontend linters. `--quiet`: only failures, verdict last. `--scope <path>`: one slice. `--<lane>` (e.g. `--read-only-fields`): one linter |
| `forge build` | Binaries + frontends. Docker images only with `--docker` or `--push` (`forge env build <env> --push` pushes each image to the reference its own workload declares — a workload's `image` is the only place a registry is set) |
| `task test` / `task test:e2e` | Unit + frontends / E2E (needs a stack up); `task test:all` adds integration |

Auth is enforced in every mode — present a real bearer token (`auth`). There is
no per-service port: one process, the binary's `server` (`_api` in every env),
serves every service on one mux (`PORT`; 8080 in a container), so the frontend
reaches all of them at one origin. Dev runs it under air (hot reload) on the
env's `<project>-dev-api` port, printed in the launch banner. Depth: `dev`,
`deploy`.

## Ship it

Asked to publish or deploy? **Reliant hosting is the default**: a new project's
`staging` and `prod` already bind its services, workers, jobs, managed Postgres
and static frontend to it:

```bash
forge env deploy prod      # build → push → release → plan → apply
forge env status prod      # then verify
```

One static site per org is free. A second static site (another env's
included) or anything that runs compute — a service, worker, job, managed
database or image build — needs billing.
Without it the deploy is **queued, not refused** and goes live once billing is set up: `env deploy` exits **7** and prints the billing link. Give
the user that link; don't retry; then verify. Under Reliant you are signed in:
never ask for `forge login`. Depth: `deploy`, `deploy/hosting`, `deploy/static-site`.

## Pre-flight: what forge already does for you

Run this list before writing code. Every row is something an earlier run
hand-wrote and then deleted.

| You are about to… | forge already does it | Load |
|---|---|---|
| add a **database entity** | mark the message `// forge:entity`, run bare `forge scaffold`. You get the create-table migration, the CRUD quintet, the managed fields, the entity struct + ORM, CRUD ops, a lifecycle test, the list/detail/new/edit pages, nav, and mock data | `db`, `proto` |
| add a **service** | `forge scaffold service <name>` — proto stub, `internal/handlers/<svc>/`, its compose.go entry (the `server` root runs it — no per-service subcommand or workload), and a test-harness entry | `services` |
| add a **custom rpc** | declare it in the proto, then `forge scaffold rpc <svc> <Name>` — a correctly-signed pb-through method plus a self-destructing test row. Never hand-write the stub | `api` |
| write a **query the generated CRUD can't express** | `internal/db/<entity>_repo_ext.go` is already there, with the repo delegates, the `db.Bun()` raw-SQL handle and `orm.Context` documented in its header | `db`, `db/crud-overrides` |
| build a **UI control** | check `src/components/` and `forge component search <thing>` first. A status badge, an enum select, and the foreign-key picker/name pair are already scaffolded and enum-aware; the library ships 70+ more | `frontend`, `frontend/pages` |
| write a **table-driven test** | `forge/pkg/tdd` + the scaffolded per-rpc `handlers_scaffold_<rpc>_test.go`; contract packages get a `mock_gen.go` you configure by field | `testing/patterns` |
| **port a utility** (errors, auth, middleware, test harness, money, retries) | `forge project libraries` lists every `forge/pkg/*` package with its purpose and source path. Adopt before porting — `forge/pkg/svcerr` is the most re-implemented thing in forge's history | `forge-libraries` |
| **read a forge library's API** | `forge project libraries <pkg>` prints every signature in one call — never `find`, never `grep`, and not `go doc <pkg>`, which shows no methods | `forge-libraries` |
| **see what a generated store offers** | `go doc ./internal/db <Entity>Store` (interfaces render in full), or `forge project shapes --kind store` for the list. Never forge's generator source | `db`, `service-layer` |
| need **dev data** | `forge db seed apply` derives FK-coherent rows from the applied schema; teach it your domain nouns in `db/seeds/vocab.yaml` | `db/seeding` |

`forge scaffold` also births a `worker`, `operator`, `crd`, `binary`, `adapter`,
`webhook`, `package`, `library`, `frontend`, `scenario` and `handler-file` —
`forge scaffold --help` for the arity of each, `services` for which to pick.
Everything scaffolded is yours from birth; re-running with nothing missing is a
clean no-op, and `--dry-run` prints the plan. Forge records none of it in
`forge.yaml`: services, workers, binaries and operators are discovered from the
code that declares them. Frontends are too: a directory under `frontends/` (or a
KCL `forge.Frontend` `path`), with per-frontend codegen knobs in the frontend's
own package.json `"forge"` block (`frontend` skill). Only packages take a config entry.

If the answer is not on this list, `forge project capabilities` has the full verb
set — check there before hand-rolling.

## Skills come from the binary

`forge skill load <name>` always matches the forge you are running. Forge never
writes skill bodies into a project. Non-reliant harnesses get a thin pointer
(`.claude/skills/forge/SKILL.md`, `.cursor/rules/forge.mdc`) that only tells the
agent to run `forge skill search <query>` / `forge skill load <name>`.

```bash
forge skill list              # the catalog: every skill, scope, one-line description
forge skill load db           # print one, authoritative
forge skill search migration  # find one by keyword
```

## Rules

- Load the skill before guessing — with `forge skill load`, not from a copy on disk.
- Check `forge project capabilities` before concluding forge has no verb for this.
- Check `forge project libraries` before writing a utility, and `go doc` to read one.
- Never hand-edit `gen/` or any `*_gen.go`. `internal/app/providers.go` and `internal/app/compose.go` are both yours to wire — forge reconciles compose.go's component set and Deps keys and leaves your value expressions alone (`forge project disown` to opt out).
- Run `forge generate` after any proto or migration change. It never touches business logic or migrations. Re-running it when nothing changed costs seconds, not minutes: a run that finds the tree byte-identical to one a full run verified as its own output says `nothing to do` and stops (`--no-cache` forces every step). So run it whenever in doubt; the minutes are spent only when something it reads actually moved.
- Parallel agents in one checkout may each run `forge generate` and `forge scaffold`. Every run takes the project's lock (`.forge/forge.lock`); a second run prints one `waiting for pid N` line and starts when the first finishes. The OS releases the lock when its holder exits, so never delete the file.
- Declare schema in migrations, never in proto; the ORM follows the schema.
- Use `forge scaffold` to scaffold — never copy-paste an existing directory.
- Hit a defect in forge itself — a command that fails, hangs or generates something wrong, or a wrong lint, skill or doc? Report it: `forge report-bug --severity <low|medium|high|critical> --title "<one line>" --actual "<exact error>" --evidence "<commands, output, paths>"` (or the `report_bug` tool of `forge mcp`). Not for bugs in the user's own code, their failing tests, or your own mistakes; never put credentials or file contents in it. It is the only way forge sends anything (`--dry-run` shows exactly what).
- Uncertain proto/migration/annotation syntax: draft it, run `forge generate`, read the error, fix. Never reverse-engineer forge's `internal/**` source.
- One service per proto package; its owned and generated files co-locate in one `internal/handlers/<svc>/`.
- Use `task test` (what CI runs), not raw `go test`; iterate on `task test:short -- ./internal/<pkg>/...`.
- Gate with `forge lint`, and never pipe it through `head`: the verdict is the LAST line. `forge lint --quiet` prints only what failed plus the verdict. `forge lint --scope internal/handlers/<svc> --scope proto/services/<svc>` lints only your slice of a shared checkout — the whole-project lanes (frontend lint, graph, component-drift, config-reach) are skipped and named, so run it unscoped before merging. Run one lane with its flag (`--read-only-fields`, `--computed-fields`, `--static-export`, `--conventions`, `--migration-safety`; `forge lint --help` lists them all), or name several (`--conventions --contract`) to run each under one verdict. An unwritten `forge:read-only` or `forge:computed` field fails lint, except while the service still holds forge's own unwired rpc stubs — then it warns `pending: implement <Rpc>`. A Next.js frontend that must be a static export (`output: static`, or bound to `forge.OnHosted`/`OnBucket`/`OnFirebase` in any env) fails `--static-export` on anything the export refuses — a `[id]` route, a server action, `cookies()`, next/image without `unoptimized` — each with its file:line and fix.
