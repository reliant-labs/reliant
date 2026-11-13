<!-- Generated from internal/benefits/benefits.yaml by `task docs:benefits`; edit the YAML, not this file.

How this page stays true:

- A PR that adds a user-visible benefit adds its entry to benefits.yaml in the
  same PR and runs `task docs:benefits`, which rewrites this page and the
  README's "Everything included" table. The PR title is the changelog entry;
  never edit CHANGELOG.md.
- TestRenderedFilesAreCurrent (internal/benefits) fails while this page or the
  README block differs from a fresh render.
- TestBenefitsEvidenceExists (internal/cli) fails an entry whose command, lint
  lane, marker, skill, library or file no longer exists.
- TestEveryGradableRuleIsABenefit (internal/cli) fails when a rule lint.rules
  can grade is in no entry's rules list, or a listed rule is not a real one, so
  an analyzer cannot merge without the bug class it prevents.
- Validate (internal/benefits) refuses an in-progress headline, a title over
  six words and an evidence item that is not exactly one kind.
-->

# What Forge gives you

A catalog of what Forge does for a project. Every entry says what the
benefit is, how you use it, and names the evidence that backs it: a
command, a lint lane, a marker, a skill, a library or a file. A test
resolves every piece of evidence against the forge binary and this
repository, so an entry whose backing code is removed fails the build
instead of going stale. It is not a tutorial:
[getting-started](getting-started.md) walks the first project and
[concepts](concepts.md) explains why Forge is shaped the way it is.

This page is generated from `internal/benefits/benefits.yaml`: to change
it, edit the YAML and run `task docs:benefits`.

**Status words.** "Enforced" means something refuses the action.
"Recorded" means Forge writes the declaration down and nothing refuses
yet. "In progress" means designed but not shipped. The page uses these
words deliberately, because a catalog that blurs them is marketing.

**Availability.** "Forge" means open-source forge on any infrastructure.
"Reliant hosting" means the benefit needs a project deployed to Reliant
hosting. "Reliant harness" means it needs the Reliant agent harness.

**Scope.** "Works in any repo" means the benefit needs no forge.yaml:
it works in an existing app in any language, and the entry says how to
start and how long an agent takes to first value. Everything else needs a
forge project; `forge skill load adopt` is how an existing app becomes one.

## Contents

1. [Works in any repo](#any-repo)
2. [Build](#build)
3. [Data](#data)
4. [Test](#test)
5. [Dev](#dev)
6. [Guardrails](#guardrails)
7. [Security](#security)
8. [Ship](#ship)
9. [Host](#host)
10. [Run](#run)
11. [Agents](#agents)

---

<a id="any-repo"></a>

## Works in any repo

What an existing app gets from forge before it is a forge project: no
forge.yaml, no generated code, any language. `forge project benefits`
prints this list for the directory you run it in.

| Benefit                                                       | Start with                                                                                                                                                                                                                                | Agent time |
| ------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| [Adopt an existing app](#adopt-existing-project)              | In the app's directory, `forge project new --in-place --kind app --dry-run` prints what it would write from the Dockerfile and compose file; without `--dry-run` it writes forge.yaml and deploy/kcl and touches none of the app's files. | ~1 hour    |
| [Migration safety checks](#migration-safety)                  | Run `forge lint --migration-safety` at the repo root; it reads `db/migrations/*.up.sql` only and flags each `.down.sql`, so scope it to new files with `--scope`.                                                                         | ~5 min     |
| [Keep your migration tool](#keep-your-migration-tool)         | Add `database: {migrations: {dir: migrations, tool: goose}}` (your directory and tool) to a forge.yaml, then run `forge lint --migration-safety`.                                                                                         | ~10 min    |
| [Real Postgres in every test](#pgtest-real-postgres)          | `go get github.com/reliant-labs/forge`, then `db, cleanup, err := pgtest.New()` in a Go test: an embedded Postgres, no Docker.                                                                                                            | ~15 min    |
| [Fast test tier stays fast](#slow-test-enforcement)           | In a Go module, run `forge lint --slow-tests`, then mark each test it names with `testkit.Slow` (`go get github.com/reliant-labs/forge`).                                                                                                 | ~15 min    |
| [Test time budgets](#test-budget)                             | In a Go module, pipe `go test -short -json ./...` into `forge ci test-budget`.                                                                                                                                                            | ~5 min     |
| [A skipped suite isn't green](#skipped-pass-detector)         | In a Go module, pipe `go test -json ./...` into `forge ci verify-test-run`.                                                                                                                                                               | ~5 min     |
| [Bug-class linters, one verdict](#one-lint-gate)              | Run `forge lint --quiet` at the repo root; lanes that need a Go module or a forge project skip and say why.                                                                                                                               | ~10 min    |
| [Timezone-safe date bucketing](#time-bucketing-lint)          | Run `forge lint --time-bucketing` in a Go module whose migrations are `db/migrations/*.up.sql` (it reads the column types from them).                                                                                                     | ~5 min     |
| [Traces that never break](#lint-rules-tracing)                | In a Go module, run `forge lint --tracing`; `--fix` rewrites each log call to its `…Context` form.                                                                                                                                        | ~10 min    |
| [Errors never leak internals](#svcerr-redaction)              | In a Go Connect service, `go get github.com/reliant-labs/forge`, add `svcerr.Interceptor()` and return `svcerr.Wrap(err)` from handlers.                                                                                                  | ~30 min    |
| [Prove parity before deploying](#env-verify-parity)           | `forge project new --in-place --kind app`, bind the workload to `forge.OnExternal {status = [...], plan = [...]}` in deploy/kcl/<env>/main.k, then `forge env verify <env> --image api=<ref>@sha256:…`.                                   | ~1 hour    |
| [Record deploys from existing CI](#record-deploys)            | Add a forge.yaml with `name:`, then after your deploy job run `forge release record --env prod --version "$TAG" --workload api --image <ref>@sha256:…` with `FORGE_CONTROL_PLANE_TOKEN` set.                                              | ~15 min    |
| [Static sites are full projects](#static-site-first-class)    | In a Vite or Next.js app, `forge project new --in-place --kind site --name <name>` writes forge.yaml and deploy/kcl and leaves your code alone.                                                                                           | ~10 min    |
| [Telemetry for any Node process](#node-telemetry)             | `npm i @reliantlabs/forge-web-runtime @opentelemetry/api`, then `initNodeTelemetry({ serviceName, serviceVersion, endpoint })` at process start.                                                                                          | ~15 min    |
| [Monitoring for any app](#monitoring-any-language)            | `forge login`, then `forge obs init prod`, and set the `OTEL_*` variables it prints in the app's environment                                                                                                                              | ~15 min    |
| [Skills that match your forge](#skills-in-the-binary)         | `forge skill load adopt`: what forge can do for this repo, with estimates to quote.                                                                                                                                                       | ~2 min     |
| [Built-in MCP server](#mcp-server)                            | `claude mcp add forge -- npx -y @reliantlabs/forge mcp` (or any MCP client); then call the `benefits` tool.                                                                                                                               | ~2 min     |
| [Hosted MCP connector](#hosted-mcp)                           | Add `https://api.reliantapi.com/forge/mcp` as a custom connector and sign in; `forge skill load connect` has the steps per client.                                                                                                        | ~2 min     |
| [Whole surface in one call](#capabilities-introspection)      | `forge project capabilities` in any directory.                                                                                                                                                                                            | ~2 min     |
| [Library API without source diving](#libraries-introspection) | In a Go module that requires `github.com/reliant-labs/forge`, `forge project libraries <pkg>`.                                                                                                                                            | ~2 min     |
| [Agents report framework bugs](#report-bug)                   | `forge report-bug --dry-run …` prints exactly what would be sent.                                                                                                                                                                         | ~2 min     |

---

<a id="build"></a>

## Build

Services, workers, jobs, operators, CLIs and frontends, scaffolded from a proto and a migration. What forge writes on the first commit is what runs in production, configured differently, and everything it scaffolds is yours to edit.

<a id="entity-from-one-marker"></a>

### One marker, a whole entity

Mark a proto message `// forge:entity` and one `forge scaffold` writes the create-table migration, the CRUD rpcs, the managed fields, the ORM projection, a lifecycle test, the list, detail, create and edit pages, nav and mock data.

_Enforced · Forge, on any infrastructure_

**Use:** `// forge:entity` above `message X`, then `forge scaffold`.

**Evidence:**

- marker `// forge:entity`
- command `forge scaffold`
- skill `db/birth` (`forge skill load db/birth`)
- command `forge scaffold entity`
- skill `db` (`forge skill load db`)

<a id="proto-to-everything"></a>

### Proto in, full stack out

One proto definition generates Connect handlers, CRUD wiring, typed frontend hooks and tests, so API, server and UI cannot disagree.

_Enforced · Forge, on any infrastructure_

**Use:** Declare the rpc in the proto, then `forge scaffold rpc <svc> <Name>` and `forge generate`.

**Why:** An rpc's logic is a `*Service` method in `internal/handlers/<svc>/rpc_<name>.go`; the scaffolded method is correctly signed and comes with a self-destructing test row, so nothing is hand-written to the stub.

**Evidence:**

- command `forge generate`
- library [`pkg/crud`](../pkg/crud)
- library [`pkg/mountkit`](../pkg/mountkit)

<a id="scaffold-verbs"></a>

### Scaffold every shape

`forge scaffold` births services, rpcs, entities, workers, operators, CRDs, adapters, webhooks, packages, binaries and frontends in the project's conventions, and is a no-op when nothing is missing.

_Enforced · Forge, on any infrastructure_

**Why:** Service (Connect RPC on the server), worker (in the server's lifecycle), binary (its own `cmd/<bin>` and Deployment) and operator (controller-runtime with CRDs) each have a scaffold and a decision table in the `binaries` and `services` skills.

**Evidence:**

- command `forge scaffold service`
- command `forge scaffold rpc`
- command `forge scaffold entity`
- command `forge scaffold worker`
- command `forge scaffold operator`
- command `forge scaffold webhook`
- command `forge scaffold adapter`
- command `forge scaffold frontend`

<a id="owned-seams-disown"></a>

### Every generated file has an exit

Generated code sits beside owned seams you edit freely, and `forge project disown` permanently hands any generated file to you.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project disown`
- command `forge project rescaffold`
- command `forge project scaffolded`

<a id="scaffold-exclude"></a>

### Areas forge never writes

`scaffold.exclude` in forge.yaml names the parts of an existing repo forge must never write, and every generate, scaffold, upgrade and rescaffold leaves them exactly as they are and says which writes it skipped.

_Enforced · Forge, on any infrastructure_

**Use:** `scaffold: {exclude: [frontends/web/, .github/workflows/deploy.yml]}`.

**Why:** A team adopting forge keeps its own frontend, workflows or docs without disowning file by file. A locked run puts back anything written under an excluded glob, and a glob over generated code other generated code imports is refused with the dependent named.

**Evidence:**

- skill `forge/migration/ownership` (`forge skill load forge/migration/ownership`)
- source [`internal/checksums/exclude.go`](../internal/checksums/exclude.go)
- source [`internal/cli/generate_exclude.go`](../internal/cli/generate_exclude.go)

<a id="prune-orphans"></a>

### Clean up after a feature

When a feature is pinned off, `forge generate` lists the files it left behind and `forge project prune --orphans` deletes the ones still exactly as forge wrote them, never an edited one.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project prune`
- skill `forge/migration/ownership` (`forge skill load forge/migration/ownership`)
- source [`internal/cli/generate_orphans.go`](../internal/cli/generate_orphans.go)

<a id="project-upgrade"></a>

### Guided forge upgrades

`forge project upgrade` moves a project to a newer forge and applies every mechanical fix for the version range, and `forge project upgrade list|apply` tracks the one-time playbooks for the rest.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project upgrade`
- command `forge project upgrade list`
- command `forge project upgrade apply`

<a id="adopt-existing-project"></a>

### Adopt an existing app

An app that already exists, in any language, keeps its code: one command, `forge project new --in-place --kind app`, reads its Dockerfile(s) and compose file (ports, Postgres, migration command, credentials) and writes only `forge.yaml` and `deploy/kcl/` (dev, staging and prod envs with a managed Postgres and a pre-rollout migration job), printing what it detected and what it assumed (`--dry-run` prints the files instead). `--claimable` sizes prod for a deploy with no account. Then work in tiers you can stop after (ship it, see it with OpenTelemetry, harden it with gated promotion and CI, and move pieces onto forge's Go stack only when that pays). `forge project new --in-place` and `forge project migrate import` bring an existing Go repository onto forge without a rewrite, with migration skills for service, CLI and library projects.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In the app's directory, `forge project new --in-place --kind app --dry-run` prints what it would write from the Dockerfile and compose file; without `--dry-run` it writes forge.yaml and deploy/kcl and touches none of the app's files. **Agent time:** ~1 hour.

**Evidence:**

- command `forge project new`
- source [`internal/cli/new_app.go`](../internal/cli/new_app.go)
- source [`internal/cli/new_app_detect.go`](../internal/cli/new_app_detect.go)
- source [`internal/generator/app.go`](../internal/generator/app.go)
- source [`internal/cli/new_app_test.go`](../internal/cli/new_app_test.go)
- command `forge project migrate import`
- skill `adopt` (`forge skill load adopt`)
- skill `migration` (`forge skill load migration`)
- skill `migration-service` (`forge skill load migration-service`)
- skill `migration-cli` (`forge skill load migration-cli`)
- skill `migration-upgrade` (`forge skill load migration-upgrade`)

<a id="adopt-frontend"></a>

### Adopt any Vite or Next app

An existing Vite or Next.js app moves under `frontends/` (or is named by a KCL path) and forge generates into it with one writer per `src/gen`, with an opt-in pnpm workspace for sharing code between web and mobile.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- skill `frontend-workspaces` (`forge skill load frontend-workspaces`)
- command `forge project new`
- skill `frontend/adopt` (`forge skill load frontend/adopt`)

<a id="rest-openapi"></a>

### REST and OpenAPI, free

`api.openapi: true` emits OpenAPI 3 from the proto in the buf pipeline and `connectrpc.com/vanguard` transcodes REST URLs onto the same Connect handlers at runtime, with no parallel codegen.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- skill `api-openapi` (`forge skill load api-openapi`)
- skill `api-rest` (`forge skill load api-rest`)
- command `forge api curl`
- skill `api/http` (`forge skill load api/http`)

<a id="service-catalog"></a>

### Browsable API inventory

`forge generate` writes `gen/forge_service_catalog.json` (`forge.dev/service-catalog/v1`): processes, the proto services each serves, methods with auth and idempotency, and components with source locations, with no server needed.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/servicecatalog`](../pkg/servicecatalog)
- source [`docs/service-catalog.md`](../docs/service-catalog.md)

<a id="lib-validate"></a>

### Request validation at the edge

`buf.validate` rules in the proto are enforced on every rpc by an interceptor, so request validation is declared once, beside the field.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/validate`](../pkg/validate)

<a id="lib-config"></a>

### Typed config from proto

Configuration is a proto message projected into KCL and into a typed Go struct, so no component parses its own environment.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/config`](../pkg/config)

<a id="web-runtime"></a>

### Frontend runtime twin of forge/pkg

`@reliantlabs/forge-web-runtime` gives a frontend typed errors with a total `.reason` vocabulary, auth and always-on `traceparent` interceptors, the app shell and a `<Resource>` table with loading, error, empty and data states and cursor pagination.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`web-runtime`](../web-runtime)
- skill `frontend-runtime` (`forge skill load frontend-runtime`)
- skill `frontend-runtime/transport` (`forge skill load frontend-runtime/transport`)
- skill `frontend-runtime/resource` (`forge skill load frontend-runtime/resource`)

<a id="frontend-runtime-config"></a>

### One bundle, every env

Frontend config is a typed `config_gen.ts` read at runtime from a per-env `config.js`, so one bundle promotes unchanged, a key not in the schema gets a did-you-mean, and build-time env inlining is a lint error.

_Enforced · Forge, on any infrastructure_

**Use:** Read config through the generated `config_gen.ts`; the platform writes `<basePath>/config.js` per env.

**Evidence:**

- lint lane `forge lint --conventions`
- skill `frontend/runtime-config` (`forge skill load frontend/runtime-config`)
- skill `frontend-runtime/config` (`forge skill load frontend-runtime/config`)

<a id="workers-lifecycle"></a>

### Workers with graceful drain

`forge scaffold worker` writes a supervised component sharing the server's typed Deps and graceful shutdown, with a lifecycle test.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge scaffold worker`
- library [`pkg/lifecyclekit`](../pkg/lifecyclekit)

<a id="lib-cron-lease"></a>

### Coordinated cron and singletons

`pkg/cronkit` runs each scheduled tick at most once across replicas that share its claim store (a missed tick is not retried), with the same scheduled time on every replica, and `pkg/lease` gives a named singleton over Postgres advisory locks with hot standby.

_Enforced · Forge, on any infrastructure_

**Use:** `forge scaffold worker <name> --kind cron --schedule "*/5 * * * *"`

**Why:** It removes the "the nightly job ran three times" bug: every replica sees the same scheduled time (`cronkit.TickTime`).

**Evidence:**

- library [`pkg/cronkit`](../pkg/cronkit)
- library [`pkg/lease`](../pkg/lease)
- skill `workers` (`forge skill load workers`)

<a id="operators-crds"></a>

### Operators and CRDs scaffolded

`forge scaffold operator` and `forge scaffold crd` give a reconciler with `pkg/controller` runtime helpers, and a StatefulSet whose declared volume grew is converged by a pre-rollout resize job.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge scaffold operator`
- library [`pkg/controller`](../pkg/controller)
- library [`pkg/appkit`](../pkg/appkit)
- library [`pkg/stsresize`](../pkg/stsresize)
- library [`pkg/cmdkit`](../pkg/cmdkit)
- command `forge scaffold crd`
- skill `binaries` (`forge skill load binaries`)
- skill `deploy/statefulset-volumes` (`forge skill load deploy/statefulset-volumes`)

<a id="cli-library-kinds"></a>

### CLIs and libraries too

`forge project new --kind cli` gives a cobra binary on `pkg/cmdkit` with per-binary typed config loadable from a file, and `--kind library` gives a Go module with no server.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project new`
- library [`pkg/cmdkit`](../pkg/cmdkit)
- skill `binaries` (`forge skill load binaries`)
- skill `binaries/config-files` (`forge skill load binaries/config-files`)
- skill `binaries/subtrees` (`forge skill load binaries/subtrees`)

<a id="adapters"></a>

### Vendor-neutral outbound adapters

`forge scaffold adapter` writes one narrow boundary per third-party system, and lint keeps it outbound-only.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge scaffold adapter`
- lint lane `forge lint --contract`

<a id="mobile-expo"></a>

### Mobile apps with Expo

`forge scaffold frontend --kind mobile` produces an Expo React Native app on the same generated Connect hooks, with an optional `ui-native` workspace package.

_Enforced · Forge, on any infrastructure_

**Why:** The app gets `app/` screens and `app:background` / `app:foreground` events.

**Evidence:**

- command `forge scaffold frontend`
- skill `ui-native-package` (`forge skill load ui-native-package`)

<a id="lib-money"></a>

### Correct money type

`forge/pkg/money` represents amounts as integer minor units plus currency, so no float rounding ever touches a price.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/money`](../pkg/money)

<a id="lib-approval"></a>

### Human approval queue

`pkg/approval` holds items an agent proposed until a person approves or rejects them, expires the undecided at their deadline and hands each decided item to the app's follow-up exactly once.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/approval`](../pkg/approval)

---

<a id="data"></a>

## Data

SQL is the schema language: the applied migrations are the single source of truth, and the entities, ORM, stores and CRUD are projections of them, so there is one declaration of every table. Forge checks each migration against what a running release and a populated table can survive before it ships.

<a id="sql-first-schema"></a>

### SQL migrations are the schema

You write plain SQL migrations; forge applies them to a real ephemeral Postgres and projects the entity structs, typed ORM, per-entity stores and CRUD wiring from the result, so the code cannot disagree with the database.

_Enforced · Forge, on any infrastructure_

**Use:** `forge generate`; `COMMENT ON TABLE t IS 'forge:no-entity'` opts a table out.

**Why:** Forge runs `db/migrations/*.up.sql` in order against a real ephemeral Postgres and generates from the result.

**Evidence:**

- library [`pkg/schemadef`](../pkg/schemadef)
- library [`pkg/orm`](../pkg/orm)
- command `forge db introspect`
- skill `db/generated-orm` (`forge skill load db/generated-orm`)
- skill `db/store` (`forge skill load db/store`)
- skill `db` (`forge skill load db`)

<a id="orm-opt-out"></a>

### Keep your own data layer

A project with a hand-written repository layer keeps forge's migrations and codegen without the generated ORM, by pinning `features: {orm: false}` in forge.yaml or marking the package `//forge:no-orm`.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- marker `// forge:no-orm`
- command `forge project features`
- skill `db/orm-and-safety` (`forge skill load db/orm-and-safety`)

<a id="migration-safety"></a>

### Migration safety checks

Every migration declares whether the previous release survives it, and `forge lint --migration-safety` refuses an unsafe column add, a statement that cannot run in a transaction, a colliding version or an undeclared rollback break before it ships.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** Run `forge lint --migration-safety` at the repo root; it reads `db/migrations/*.up.sql` only and flags each `.down.sql`, so scope it to new files with `--scope`. **Agent time:** ~5 min.

**Use:** Start each migration with `-- forge:backward-compatible — <why>` or `-- forge:breaks-rollback <why>`; run `forge lint --migration-safety`, or `forge ci migration-safety` in CI. A pragma in the migration file suppresses one rule beside the SQL it is about.

**Why:** A migration that a populated table or the previous release cannot survive passes every test on a fresh database and fails on the first real one.

**Evidence:**

- command `forge lint --migration-safety`
- command `forge ci migration-safety`
- lint lane `forge lint --migration-safety`
- source [`internal/linter/migrationlint`](../internal/linter/migrationlint)
- skill `db` (`forge skill load db`)
- source [`internal/linter/migrationlint/compat_declaration.go`](../internal/linter/migrationlint/compat_declaration.go)
- source [`internal/linter/migrationlint/add_column.go`](../internal/linter/migrationlint/add_column.go)
- source [`internal/linter/migrationlint/transaction_block.go`](../internal/linter/migrationlint/transaction_block.go)
- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)

| Rule                                  | Bug class it prevents                                                                                                                              | Lane                 |
| ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------- |
| `no-down-migration`                   | A down migration claims to undo a release and cannot: by the time anyone would run it, rows are in the new shape and other services have read them | `--migration-safety` |
| `duplicate-migration-version`         | Two files claiming one version, so whichever reached a shared database first leaves the other permanently unapplied                                | `--migration-safety` |
| `non-timestamp-migration-version`     | Sequential numbering makes parallel branches collide; a UTC timestamp cannot without two allocations in one second                                 | `--migration-safety` |
| `migration-version-below-merged-head` | A new migration numbered at or below the default branch's head is invisible: every already-migrated database never runs it                         | `--migration-safety` |
| `destructive-change`                  | A `DROP`/`TRUNCATE`-class statement; split it into data-preserving steps or allow it by `forge:allow-destructive`                                  | `--migration-safety` |
| `unsafe-add-not-null-column`          | `ADD COLUMN … NOT NULL` without a default fails on a populated table                                                                               | `--migration-safety` |
| `set-not-null-without-backfill`       | `SET NOT NULL` with no earlier backfill fails on a populated table                                                                                 | `--migration-safety` |
| `volatile-default`                    | A volatile `DEFAULT` in `ADD COLUMN` can rewrite and lock a populated table                                                                        | `--migration-safety` |
| `non-transactional-statement`         | `CREATE INDEX CONCURRENTLY` and kin cannot run in a transaction block; the statement must be alone in its file                                     | `--migration-safety` |
| `enum-value-used-before-commit`       | Using an enum value the same file adds fails with SQLSTATE 55P04                                                                                   | `--migration-safety` |
| `invalid-adopts-directive`            | A `-- forge:adopts` baseline declaration the migrator would refuse at boot on every database                                                       | `--migration-safety` |
| `migration-compat-conflict`           | A migration that declares both `forge:backward-compatible` and `forge:breaks-rollback`                                                             | `--migration-safety` |
| `migration-compat-reason`             | A compatibility declaration with no reason, so the rollback decision that reads it has nothing to go on                                            | `--migration-safety` |
| `migration-compat-undeclared`         | A migration that says neither `forge:backward-compatible` nor `forge:breaks-rollback`, which a rollback decision and the release guard read        | `--migration-safety` |

<a id="keep-your-migration-tool"></a>

### Keep your migration tool

A team on goose, atlas, flyway, dbmate, golang-migrate or its own command keeps it and its migrations directory: forge runs that tool to build the schema it projects the ORM and seeds from, lints every SQL file it applies, and runs it from `forge db migrate up` — no protos, no conversion.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** Add `database: {migrations: {dir: migrations, tool: goose}}` (your directory and tool) to a forge.yaml, then run `forge lint --migration-safety`. **Agent time:** ~10 min.

**Use:** `database: {migrations: {dir: migrations, tool: goose}}` in forge.yaml; `tool: custom` with `apply: [...]` runs any command.

**Why:** Moving a live history onto a new migrator is the step teams put off, so forge's schema features would wait behind it. Running the team's own tool against forge's throwaway Postgres gives the same applied schema without the move.

**Evidence:**

- source [`internal/migrationhook`](../internal/migrationhook)
- source [`internal/shadowdb/hook.go`](../internal/shadowdb/hook.go)
- source [`internal/linter/migrationlint/tools.go`](../internal/linter/migrationlint/tools.go)
- command `forge db migrate up`
- skill `db/own-migration-tool` (`forge skill load db/own-migration-tool`)

<a id="managed-postgres-backups"></a>

### Backups and point-in-time restore

A hosted database is built for continuous WAL archiving and a daily base backup to object storage, and restoring is a declaration: a new `ManagedDatabase` with `restore = {sourceDatabase, pointInTime}` boots from the source's backups replayed to that moment.

_Enforced · Reliant hosting_

**Use:** `forge.ManagedDatabase { spec.restore = forge.DatabaseRestore { sourceDatabase = "db", pointInTime = "2026-11-10T14:05:00Z" } }`, then `forge env deploy <env>`.

**Why:** A restore applies once, at creation, and never overwrites the source, so a mistaken one cannot destroy the original; there is no restore button or command. Each database's archive is written by its own workload identity with access to its own stream only. Claimable (unclaimed) databases are not backed up, and a database reports that its backups are not active until the environment's backup store is ready: read deploy status to see whether yours are.

**Evidence:**

- `internal/operators/manageddatabase/backup.go` in the control-plane repo
- `internal/operators/manageddatabase/backup_streams.go` in the control-plane repo
- `internal/operators/manageddatabase/backup_identity.go` in the control-plane repo
- `internal/operators/manageddatabase/backupgcs` in the control-plane repo
- `deploy/kcl/lib/mdb_backups.k` in the control-plane repo
- `deploy/kcl/lib/hosted_deploy.k` in the control-plane repo
- source [`pkg/deploy/v1alpha1/manageddatabase_types.go`](../pkg/deploy/v1alpha1/manageddatabase_types.go)
- source [`kcl/tiers/tiers_gen.k`](../kcl/tiers/tiers_gen.k)

<a id="schema-behavior-from-columns"></a>

### Behavior from real columns

`created_at` and `updated_at` columns give managed timestamps, a time-typed `deleted_at` gives soft delete and text columns give generated list search, so behaviour comes from the schema rather than from annotations.

_Enforced · Forge, on any infrastructure_

**Use:** `COMMENT ON COLUMN t.deleted_at IS 'forge:no-soft-delete'` makes `deleted_at` a plain archive stamp.

**Evidence:**

- library [`pkg/orm`](../pkg/orm)

<a id="write-policy-markers"></a>

### Write policy beside the column

`forge:read-only`, `forge:computed`, `forge:generated`, `forge:guards`, `forge:immutable`, `forge:version` (optimistic concurrency) and `forge:append-only` declare who may write a column, and lint makes a field nothing writes a build failure.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project annotations`
- lint lane `forge lint --read-only-fields`
- skill `db/write-policy` (`forge skill load db/write-policy`)

<a id="append-only-ledgers"></a>

### Append-only ledgers, enforced twice

`forge:append-only` removes Update/Delete from the store and installs a trigger rejecting every UPDATE and DELETE.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- skill `db/append-only` (`forge skill load db/append-only`)

<a id="tx-after-commit"></a>

### Transactions state their correctness

`RunTx` has no default isolation — SERIALIZABLE with retry or a lock under READ COMMITTED, never both — and `orm.AfterCommit` holds side effects until commit.

_Enforced · Forge, on any infrastructure_

**Use:** forge fix tx-isolation

**Evidence:**

- library [`pkg/orm`](../pkg/orm)
- lint lane `forge lint --conventions`
- command `forge fix tx-isolation`
- skill `service-layer/transactions` (`forge skill load service-layer/transactions`)

<a id="never-waiting-migrations"></a>

### Migrations that never wedge prod

A migration never waits for a lock without a limit (`lock_timeout` 5s, five attempts with backoff, `statement_timeout` 30m, overridable per file), and `CREATE INDEX CONCURRENTLY` cleans up an invalid index, so one slow reader plus one `ALTER TABLE` is not an outage.

_Enforced · Forge, on any infrastructure_

**Use:** `-- forge:lock-timeout` and `-- forge:statement-timeout` override the limits for one file.

**Evidence:**

- command `forge db migrate up`
- library [`pkg/migratekit`](../pkg/migratekit)
- skill `db/migration-timeouts` (`forge skill load db/migration-timeouts`)
- skill `db/deploy-migrations` (`forge skill load db/deploy-migrations`)

<a id="migration-tooling"></a>

### Migration lifecycle commands

`forge db migration new` allocates a timestamp version, `forge db migration rebase` re-versions a branch's migrations after everything merged, and `forge db migrate` and `forge db squash` apply, inspect and compact them.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge db migration new`
- command `forge db migration rebase`
- command `forge db migrate status`
- command `forge db squash`
- library [`pkg/migratekit`](../pkg/migratekit)
- skill `db/versioning` (`forge skill load db/versioning`)

<a id="seed-data"></a>

### Realistic seed data

`forge db seed apply` materializes deterministic, foreign-key-coherent rows from the applied schema, satisfying CHECKs, UNIQUEs, composite keys and status lifecycles, and learns your nouns from `db/seeds/vocab.yaml`.

_Enforced · Forge, on any infrastructure_

**Use:** `forge db seed apply|reset|status`

**Evidence:**

- command `forge db seed apply`
- command `forge db seed reset`
- library [`pkg/seedplan`](../pkg/seedplan)
- skill `db/seeding` (`forge skill load db/seeding`)
- command `forge db seed status`

<a id="dev-db-recovery"></a>

### Wedged dev DB recovers

`forge db migrate force` repairs and clears dirty migration state, and `forge db reset` drops, recreates, migrates and seeds a dev database (dev only).

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge db migrate force`
- command `forge db reset`
- skill `db/recovery` (`forge skill load db/recovery`)

<a id="db-dashboards-privileges"></a>

### Dashboards bounded by DB privileges

Dashboard panels read through a `forge_dash` role granted exactly their columns by migration, with row cap, timeout and concurrency limit.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/dbbrowse`](../pkg/dbbrowse)
- library [`pkg/dbgrants`](../pkg/dbgrants)
- skill `db/dashboards` (`forge skill load db/dashboards`)

<a id="object-storage-bucket"></a>

### Object storage per env

Declare a `forge.Bucket` per env (MinIO in dev, any S3-compatible bucket elsewhere) and `pkg/blobstore` presigns uploads that go from the browser straight to the bucket, so file bytes never pass through your API.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/blobstore`](../pkg/blobstore)
- skill `storage/blobstore` (`forge skill load storage/blobstore`)

<a id="managed-buckets"></a>

### Managed object storage

A declared `forge.Bucket` becomes a platform-owned bucket in hosted envs (MinIO in dev), with credentials and CORS origins managed for you.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/managedbucket` in the control-plane repo
- `internal/operators/workload/bucket.go` in the control-plane repo

<a id="managed-postgres-restore-drill"></a>

### Restores are drilled, not assumed

An automated drill takes a real backup, destroys the cluster and volumes, restores to a timestamp, and asserts rows before it came back and rows after it did not.

_Enforced · Reliant hosting_

**Why:** The first drill attempt failed ("Expected empty archive") because a restored cluster archived into its source's stream; each restore now gets its own stream (TestSecondRestoreGetsItsOwnStream).

**Evidence:**

- `internal/operators/manageddatabase/restore_drill_integration_test.go` in the control-plane repo

---

<a id="test"></a>

## Test

Three test tiers with the subject real at every tier: generated mocks for unit tests, a real Postgres for integration tests and a running stack for end-to-end tests. Slow tests say so, so the fast tier stays fast.

<a id="pgtest-real-postgres"></a>

### Real Postgres in every test

Database tests run against a real embedded Postgres shared machine-wide, each test on its own copy of a migrated template, with no Docker and no database mocks, so they belong in the fast tier.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** `go get github.com/reliant-labs/forge`, then `db, cleanup, err := pgtest.New()` in a Go test: an embedded Postgres, no Docker. **Agent time:** ~15 min.

**Use:** `testkit.NewPostgresDB(t)`, or `pkg/pgtest` directly.

**Evidence:**

- library [`pkg/pgtest`](../pkg/pgtest)
- library [`pkg/testkit`](../pkg/testkit)
- skill `testing` (`forge skill load testing`)

<a id="generated-mocks"></a>

### A generated mock per interface

Every interface in a package's `contract.go` gets a recording mock in `mock_gen.go`, configured per method by field, so unit tests never hand-roll a fake.

_Enforced · Forge, on any infrastructure_

**Use:** Set `MockService.<Method>Func` in a test; `forge generate` keeps the mock in step with the interface.

**Evidence:**

- library [`pkg/contractkit`](../pkg/contractkit)
- source [`internal/generator/contract`](../internal/generator/contract)
- skill `contracts` (`forge skill load contracts`)
- command `forge fix harness-mocks`

<a id="test-tiers"></a>

### Three tiers, real subject

Unit tests use the real subject plus generated mocks, integration tests a real migrated Postgres and end-to-end tests a running dev stack, so the subject under test stays real at every tier.

_Enforced · Forge, on any infrastructure_

**Use:** `task test:short -- ./internal/<pkg>/...` while iterating, `task test` once at the end, `task test:integration`, `task test:e2e`.

**Evidence:**

- skill `testing` (`forge skill load testing`)

<a id="slow-test-enforcement"></a>

### Fast test tier stays fast

A test that execs a tool, drives a container or dials the network calls `testkit.Slow` and skips under `-short`, and `forge lint --slow-tests` fails one that does not, so the fast tier stays fast.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In a Go module, run `forge lint --slow-tests`, then mark each test it names with `testkit.Slow` (`go get github.com/reliant-labs/forge`). **Agent time:** ~15 min.

**Use:** `testkit.Slow(t, "<why>", testkit.Inputs("deploy/**"))` first thing in the test.

**Why:** A plain `if testing.Short() { t.Skip(...) }` also counts. Two guards hold the line because neither sees everything: `forge lint --slow-tests` before a test ever runs, and `go test -short -json ./... | forge ci test-budget` after.

**Evidence:**

- command `forge lint --slow-tests`
- source [`internal/linter/slowtest`](../internal/linter/slowtest)
- library [`pkg/testkit`](../pkg/testkit)

<a id="cache-soundness"></a>

### No stale cached passes

A test whose child process reads repo files must skip under `-short` or open those inputs itself, so Go's test cache cannot replay a PASS after the edit that broke it.

_Enforced · Forge, on any infrastructure_

**Why:** `forge-slow-test-inputs` reads the declared inputs from source. Never set a private `GOCACHE`, and do not add `-count=1` or `-race` to inner-loop runs: they defeat a cache that is correct for hermetic tests.

**Evidence:**

- command `forge lint --slow-tests`
- library [`pkg/testkit`](../pkg/testkit)
- skill `testing` (`forge skill load testing`)

<a id="test-budget"></a>

### Test time budgets

`forge ci test-budget` reads a `go test -json` run and flags unmarked tests that blew the fast-tier budget.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In a Go module, pipe `go test -short -json ./...` into `forge ci test-budget`. **Agent time:** ~5 min.

**Evidence:**

- command `forge ci test-budget`
- source [`internal/cli/ci_test_budget.go`](../internal/cli/ci_test_budget.go)

<a id="skipped-pass-detector"></a>

### A skipped suite isn't green

`forge ci verify-test-run` reports packages that skipped so much that their pass proves nothing.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In a Go module, pipe `go test -json ./...` into `forge ci verify-test-run`. **Agent time:** ~5 min.

**Evidence:**

- command `forge ci verify-test-run`

<a id="tdd-library"></a>

### Table-driven tests, batteries included

`pkg/tdd` gives table-driven helpers for rpcs, the database, mocks and e2e, every scaffolded rpc gets a test row to start from, and `forge project migrate tdd` converts hand-rolled tables.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`pkg/tdd/rpc.go`](../pkg/tdd/rpc.go)
- source [`pkg/tdd/db.go`](../pkg/tdd/db.go)
- source [`pkg/tdd/mock.go`](../pkg/tdd/mock.go)
- source [`pkg/tdd/e2e.go`](../pkg/tdd/e2e.go)
- command `forge project migrate tdd`
- lint lane `forge lint --tests`
- skill `testing/patterns` (`forge skill load testing/patterns`)
- skill `testing/flow` (`forge skill load testing/flow`)

<a id="entity-lifecycle-tests"></a>

### CRUD tests come free

Scaffolding an entity also writes its CRUD lifecycle test, so new tables are tested from their first commit.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge scaffold entity`
- command `forge lint --fixture-drift`

<a id="flow-tests-seedgraph"></a>

### Flow tests over real data

`seedplan.SeedGraph` seeds the foreign-key spine so a flow test drives the real service against a migrated database.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/seedplan`](../pkg/seedplan)
- skill `testing/flow` (`forge skill load testing/flow`)

<a id="frontend-hook-tests"></a>

### Generated hooks are tested

Lint requires a test beside every generated hooks file and fails a hook test whose assertions can never pass, such as a query-shaped test on a mutation hook.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --tests`
- lint lane `forge lint --tests`
- lint lane `forge lint --conventions`

<a id="kcl-eval-tests"></a>

### Test what you declared

`forge/pkg/kcleval` lets a Go test assert against the project's KCL declarations instead of restating them.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/kcleval`](../pkg/kcleval)
- command `forge kcl eval`

---

<a id="dev"></a>

## Dev

One command brings up the whole stack on your machine, under hot reload, with every log in one directory. Each git worktree gets a stack of its own, so parallel agents never share a port or a database.

<a id="env-up-dev"></a>

### The whole stack, one command

`forge env up dev` builds, launches and hot-reloads every service, worker and frontend with Postgres, the IdP, the observability stack and mail ready before anything dials them, and the scaffolded `dev` needs neither Docker nor a cluster.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env up dev [--no-build] [--target <workload>] [-D name=value] [--background|--watch]`

**Evidence:**

- command `forge env up`
- command `forge env up --target`
- skill `dev` (`forge skill load dev`)
- command `forge env status`
- command `forge env down`

<a id="per-worktree-devstacks"></a>

### A dev stack per worktree

Every checkout gets its own port block and its own data, so ten worktrees (or ten agents) run ten dev stacks side by side without sharing a port or a database.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env ps` lists every stack on the machine; `forge env devstack list|port|prune|release` manage the blocks.

**Why:** `allocate_port(base, key)` derives each port from the worktree's block, and the default ceiling is `max_stacks: 8`.

**Evidence:**

- command `forge env devstack list`
- command `forge env devstack port`
- command `forge env devstack prune`
- source [`internal/cli/devstack.go`](../internal/cli/devstack.go)
- lint lane `forge lint --conventions`
- source [`internal/config/config.go`](../internal/config/config.go)
- skill `dev` (`forge skill load dev`)

<a id="browser-logs-on-disk"></a>

### Browser console logs on disk

Every `console.*` call, uncaught error and unhandled rejection from a scaffolded frontend lands in `.forge/logs/<env>/frontend_<name>.log`, prefixed `[browser:<level>]`, beside the backend logs, so what the browser did is a grep.

_Enforced · Forge, on any infrastructure_

**Use:** `grep '\[browser:error\]' .forge/logs/dev/*.log`

**Why:** An uncaught `TypeError` has no `console` call in your source; it is in that file.

**Evidence:**

- source [`internal/templates/frontend/vite-spa/src/lib/devlog-plugin.ts`](../internal/templates/frontend/vite-spa/src/lib/devlog-plugin.ts)
- skill `debug` (`forge skill load debug`)
- skill `frontend-runtime/telemetry` (`forge skill load frontend-runtime/telemetry`)
- source [`internal/templates/frontend/nextjs/src/app/%5F_forge/log/route.ts`](../internal/templates/frontend/nextjs/src/app/%5F_forge/log/route.ts)

<a id="mock-mode"></a>

### Build UI before the backend

`mock_api` answers every rpc from its descriptor with deterministic, schema-valid messages (streams included), named scenarios stub specific calls, and fixtures are checked for freshness, so a UI is built and tested before the backend exists.

_Enforced · Forge, on any infrastructure_

**Use:** `mock_api` = `""`, `true` or `hybrid`; `forge scaffold scenario` stubs specific rpcs.

**Evidence:**

- source [`internal/templates/frontend/mocks/mock-transport.ts.tmpl`](../internal/templates/frontend/mocks/mock-transport.ts.tmpl)
- command `forge scaffold scenario`
- source [`internal/templates/frontend/mocks/fixture-freshness.test.ts.tmpl`](../internal/templates/frontend/mocks/fixture-freshness.test.ts.tmpl)
- source [`internal/templates/frontend/mocks/scenarios`](../internal/templates/frontend/mocks/scenarios)
- skill `frontend/mock-mode` (`forge skill load frontend/mock-mode`)

<a id="hot-reload"></a>

### Hot reload per workload

A host workload runs under `air`, `go-run` or a binary runner, and `--target` scopes a whole run to one workload.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge env up`

<a id="up-timings"></a>

### Every dev boot measured

Each `forge env up` prints a phase-timing table and appends one JSON line to `.forge/logs/<env>/up-timings.jsonl`, so a dev-loop speedup is measured before it is claimed.

_Enforced · Forge, on any infrastructure_

**Why:** Each line is a `forge.env.up.timings/v1` document.

**Evidence:**

- source [`internal/cli/up_timings.go`](../internal/cli/up_timings.go)

<a id="env-down-self-safe"></a>

### Stopping never kills yourself

`forge env down` walks its own parent chain first, so it can't end the agent or shell that started it.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge env down`

<a id="env-exec-config"></a>

### Inspect and exec into envs

`forge env config` prints what an environment hands each workload, `forge env exec` runs a command with it, `forge env options` lists the render options and `forge doctor parity` diffs host mode against cluster mode.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge env config`
- command `forge env ps`
- command `forge env exec`
- command `forge env diff`
- command `forge env options`

<a id="generate-cache-lock"></a>

### Fast, concurrency-safe codegen

`forge generate` never overwrites hand-written logic, finishes in seconds with `nothing to do` on an unchanged tree, and queues parallel runs on a project lock the OS releases when the holder exits.

_Enforced · Forge, on any infrastructure_

**Use:** `forge generate`; `--no-cache` forces a full run. The lock is `.forge/forge.lock`.

**Evidence:**

- command `forge generate`
- source [`internal/cli/generate_cache.go`](../internal/cli/generate_cache.go)
- skill `forge` (`forge skill load forge`)

<a id="heavy-slot-scheduler"></a>

### Machine-wide heavy-work queue

Builds, test lanes, codegen and full lints queue for a host-wide slot (default min(CPUs/2, memory GiB/8)), so many agents or terminals on one machine never thrash it, and a crashed holder never strands a slot.

_Enforced · Forge, on any infrastructure_

**Use:** `forge slot run`, `forge slot status`, `FORGE_HEAVY_SLOTS`

**Why:** Measured on a 16 vCPU host, `go build ./...` took 36-50s beside 0-3 other heavy jobs and 101-155s beside 8 or more, and a full local gate went from about 40 minutes to about 2.3 hours when everything ran at once.

**Evidence:**

- command `forge slot run`
- command `forge slot status`
- library [`pkg/hostslot`](../pkg/hostslot)
- skill `ci/slots` (`forge skill load ci/slots`)

<a id="storage-gc"></a>

### Disk kept in check

`forge storage` reports host capacity, refuses a build below the free-space reserve, trims Go build caches and removes idle, clean, pushed worktrees.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge storage status`
- command `forge storage gc`
- command `forge storage worktrees`

<a id="debugger-cli"></a>

### Debugger from the command line

`forge debug` drives Delve against a running service (breakpoints, stepping, locals, goroutines) from a terminal or an agent.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge debug start`
- command `forge debug break`
- command `forge debug goroutines`

<a id="api-curl"></a>

### Call any rpc from the CLI

`forge api curl` prints a copy-pasteable curl for any rpc of the project.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge api curl`

<a id="local-k3d-cluster"></a>

### Local Kubernetes when you need it

`forge cluster up` gives a local k3d cluster with ingress URLs, logs and reload, and per-worktree namespaces share one cluster behind a kubectl-context pin.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge cluster up`
- command `forge cluster urls`
- command `forge cluster logs`

<a id="doctor"></a>

### Environment doctor

`forge doctor` checks the toolchain, payload caps, probes, resource requests and limits of every declared env with nothing running, and `forge doctor parity` checks host mode against cluster mode.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge doctor`
- command `forge doctor parity`

<a id="pinned-tools"></a>

### Pinned toolchain, auto-installed

`forge tools install` installs exactly the tool versions the project pins, so "works on my machine" stops being a variable.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge tools install`
- command `forge tools pins`

<a id="component-library"></a>

### A 70+ component library

`forge component search` and `forge component install` add ready-made frontend components, and scaffolded pages already carry enum-aware status badges and foreign-key pickers.

_Enforced · Forge, on any infrastructure_

**Use:** `forge component search|install|list`

**Evidence:**

- command `forge component search`
- command `forge component install`
- skill `frontend/components` (`forge skill load frontend/components`)
- skill `frontend/design/verify` (`forge skill load frontend/design/verify`)
- command `forge component list`

<a id="cloud-dev-machines"></a>

### Cloud dev machines

Agents and forge stacks can run on cloud machines that wake on demand and suspend when idle, with docker and k3d available.

_Enforced · Reliant harness_

**Evidence:**

- `docs/machines/cloud.mdx` in the reliant repo
- `internal/operators/workspace` in the control-plane repo

---

<a id="guardrails"></a>

## Guardrails

`forge lint` is one report-only gate over forge's own analyzers and the standard linters, with one verdict: it writes nothing unless `--fix`, and every fixer's rewrite is type-checked and taken back if it breaks its package. Each forge rule names the bug class it prevents, and the lint-family entries below list every rule `lint.rules` can grade with the flag that runs it alone; every lane except the opt-in ones also runs in a bare `forge lint`. A rule new in a release warns before it gates. Related checks outside `forge lint`: `forge doctor`, `forge ci verify-generated`, `forge ci test-budget`, `forge ci vuln-scan` and `forge ci validate-kcl`.

<a id="one-lint-gate"></a>

### Bug-class linters, one verdict

`forge lint` runs forge's analyzers, each rule naming the bug class it prevents, beside golangci-lint, buf, eslint and the typecheck, and prints one verdict last.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** Run `forge lint --quiet` at the repo root; lanes that need a Go module or a forge project skip and say why. **Agent time:** ~10 min.

**Use:** `forge lint`; `--quiet` prints only what failed, `--scope <path>` lints a slice of a shared checkout, `--json` and `--gate-json` are for machines, `--skip-frontends` is a backend-only gate, and `lint.frontend.typecheck` dials the frontend typecheck.

**Why:** `lint.rules` in `forge.yaml` grades every forge rule `error`, `warn` or `off` identically in every output mode. Never pipe `forge lint` through `head`: the verdict is the last line.

**Evidence:**

- command `forge lint`
- command `forge lint --scope`
- source [`internal/cli/lint`](../internal/cli/lint)

<a id="local-ci-gate"></a>

### The whole CI gate, locally

`forge ci gate` (`task ci`) runs verify-generated, lint, buf, migration safety, frontend checks, build, vulnerability and license scans, KCL validation, tests and integration tests in one table, and a skipped check fails the gate.

_Enforced · Forge, on any infrastructure_

**Use:** `task ci`; `CI_SKIP=<check>` accepts a named skip, `CI_FULL=1` unit-tests every package.

**Why:** The CI workflow is a thin wrapper around the same tasks, so "CI did not run" never has to mean "unverified". `forge ci gate --list` names the checks, a `CI_SKIP` name that matches no check is an error, tools are pinned so a copy on PATH at another version is never used, verdicts are cached on an unchanged committed tree (`PASS (cached <sha>)`), and a branch unit-tests only the packages its change reaches.

**Evidence:**

- command `forge ci gate`
- source [`internal/cigate`](../internal/cigate)
- command `forge ci run`
- command `forge ci summarize`
- command `forge gate record`
- skill `ci/gate` (`forge skill load ci/gate`)

<a id="ci-workflow-opt-out"></a>

### Keep your own workflows

`ci.workflows` in forge.yaml switches single forge workflows off (deploy, build-images, e2e, ...), and a same-named workflow the repo wrote itself is never overwritten or silently adopted: forge warns until forge.yaml says so.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- skill `ci/extending` (`forge skill load ci/extending`)
- source [`internal/config/ci_workflows.go`](../internal/config/ci_workflows.go)
- source [`internal/generator/ci_workflows.go`](../internal/generator/ci_workflows.go)

<a id="generated-drift"></a>

### Stale codegen can't merge

`forge ci verify-generated` regenerates in a scratch copy and fails when the committed output differs, and `forge lint --generated-drift` fails a generated file someone edited by hand.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge ci verify-generated`
- command `forge lint --generated-drift`
- source [`internal/cli/ci_verify_scratch.go`](../internal/cli/ci_verify_scratch.go)

<a id="lint-rollout-grace"></a>

### New rules warn before gating

A lint rule new in a forge release warns, with a "new in vX; gates from vY" note, and gates only from the next release, so bumping the forge pin never turns your build red overnight.

_Enforced · Forge, on any infrastructure_

**Why:** Every gradable rule has an `IntroducedIn` and a `GatesFrom` row in the rollout table, and `TestEveryGradableRuleDeclaresItsRollout` fails a rule without one. `lint.rules` wins in both directions: `{rule: error}` opts in early, `{rule: warn}` extends the grace period. The verdict line counts the warnings, so a green run never hides that a pin bump will turn red. A rule that must gate on arrival breaks every pinned consumer, so it is the owner's call.

**Evidence:**

- source [`internal/cli/lint/lint_rollout_table.go`](../internal/cli/lint/lint_rollout_table.go)
- source [`internal/cli/lint/lint_rollout.go`](../internal/cli/lint/lint_rollout.go)
- skill `ci/lint-rollout` (`forge skill load ci/lint-rollout`)

<a id="suppression-ledger"></a>

### Every suppression, with its reason

A suppression is a comment that must give its reason, and `forge lint --show-suppressed` lists every one the project declares with file, line and stated reason, so waivers stay visible.

_Enforced · Forge, on any infrastructure_

**Use:** `// forge:lint-disable-next-line <rule>: <why>` (the `forge:lint-disable-*` family)

**Evidence:**

- command `forge lint --show-suppressed`
- source [`internal/linter/suppress`](../internal/linter/suppress)

<a id="lint-skip-is-failure"></a>

### A skipped lane never passes

A gating lint lane that could not run fails the build unless you accept the skip by name with a reason, and `--strict` escalates advisory findings to errors.

_Enforced · Forge, on any infrastructure_

**Use:** `forge lint --allow-skip LANE=REASON`

**Evidence:**

- command `forge lint --allow-skip`
- command `forge lint --strict`

<a id="lint-lanes-off"></a>

### Switch a lint lane off

`lint.lanes` in forge.yaml switches whole `forge lint` lanes off (frontend, migration safety, db grants, ...) for a repo that checks them another way, and the verdict still names every lane that is off.

_Enforced · Forge, on any infrastructure_

**Use:** `lint: {lanes: {frontend: off, migration-safety: off}}`

**Evidence:**

- skill `ci/lint-rollout` (`forge skill load ci/lint-rollout`)
- source [`internal/cli/lint/lint_lanes.go`](../internal/cli/lint/lint_lanes.go)

<a id="lint-autofix"></a>

### Type-checked auto-fixes

`forge lint --fix` applies the mechanical fixes (formatting, traced logging, prettier, eslint) and takes back any Go rewrite that breaks its package, and `--check` or `--no-fix` print each pending fix's diff and fail.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --fix`
- command `forge fix`

<a id="codemod-fixers"></a>

### Codemods for convention moves

`forge fix` rewrites a project onto a new convention in one command (Connect errors, narrow Deps, transaction isolation, harness mocks, the composition graph, operatorkit, the config proto package), and takes back a rewrite that fails.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge fix connect-errors`
- command `forge fix narrow-deps`
- command `forge fix tx-isolation`
- command `forge fix harness-mocks`
- command `forge fix composition-graph`
- command `forge fix operatorkit`
- command `forge fix config-proto-package`

<a id="version-skew-guard"></a>

### Pinned forge, no skew

Forge refuses to lint or generate with a binary other than the one `forge.yaml` pins, so every machine and CI produce the same output.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --allow-version-skew`
- command `forge tools pins`

<a id="unwritten-fields"></a>

### Catches fields nothing writes

A read-only or computed field that no code populates fails lint, instead of every row shipping a silent zero.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --read-only-fields`
- command `forge lint --computed-fields`
- lint lane `forge lint --read-only-fields`
- lint lane `forge lint --computed-fields`

<a id="guarded-field-bypass"></a>

### Guarded fields stay guarded

Lint flags an edit page or handler that writes a `forge:guards` column directly, bypassing the rpc that owns its state transitions.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --guarded-fields`
- lint lane `forge lint --guarded-fields`

<a id="create-nullability"></a>

### Create requests keep presence

Lint fails when a field's optional label differs between an entity and its Create request, which would silently drop "unset" vs "zero".

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --create-nullability`
- lint lane `forge lint --create-nullability`

<a id="list-filter-optional"></a>

### List filters that actually filter

A List filter field without `optional` has no presence, so "no filter" would collapse into "filter by zero"; lint rejects it.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- lint lane `forge lint --conventions`

<a id="closed-set-check"></a>

### Enums and CHECKs agree

A database CHECK marked `forge:closed-set` must list exactly the Go set it mirrors, and lint prints the forward migration that fixes a mismatch.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --closed-sets`

<a id="db-grants-lint"></a>

### Credential columns need a decision

A token-, password- or key-shaped column must be marked secret or readable with a reason before the read-only database role may see it.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --db-grants`
- library [`pkg/dbgrants`](../pkg/dbgrants)

<a id="time-bucketing-lint"></a>

### Timezone-safe date bucketing

Lint flags a two-argument `date_trunc` over a `TIMESTAMPTZ`, which truncates in the session timezone and moves every daily total with the deploy host.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** Run `forge lint --time-bucketing` in a Go module whose migrations are `db/migrations/*.up.sql` (it reads the column types from them). **Agent time:** ~5 min.

**Use:** `date_trunc('day', col, 'UTC')`

**Evidence:**

- command `forge lint --time-bucketing`
- skill `db/time-buckets` (`forge skill load db/time-buckets`)

<a id="bun-placeholder-lints"></a>

### Raw-SQL placeholder bugs caught

Lint catches `$1`-style placeholders and literal `?` in bun raw SQL, both of which fail at runtime or bind shifted arguments.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- lint lane `forge lint --conventions`

<a id="fixture-drift-lint"></a>

### Test fixtures checked against schema

`forge lint --fixture-drift` runs every scaffolded CRUD fixture statement against a shadow Postgres and reports what the current schema rejects.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --fixture-drift`

<a id="contract-lint"></a>

### Package contracts enforced

An internal package with exported methods and no `contract.go` is flagged, so every component is visible to wiring, mocks, the observability decorator and the reporters.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --contract`
- source [`internal/linter/contract`](../internal/linter/contract)
- lint lane `forge lint --contract`

<a id="deps-boundary-lints"></a>

### Dependency injection stays honest

Lint keeps Deps interface-typed and narrow, forbids downcasts and container-style deps, and pushes scalars into typed config, so every component unit-tests against mocks.

_Enforced · Forge, on any infrastructure_

**Use:** `forge fix narrow-deps` narrows a Deps interface to the methods the package uses.

**Evidence:**

- lint lane `forge lint --contract`
- lint lane `forge lint --conventions`
- command `forge lint --config-deps`

<a id="optional-deps-guard"></a>

### No nil-dep panics

Lint flags an unguarded dereference of a Deps field marked `forge:optional-dep`, a nil-pointer panic no construction-time check can catch.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --optional-deps-guard`
- lint lane `forge lint --conventions`

<a id="composition-graph-lint"></a>

### Wiring checked as a graph

`forge lint --graph` fails a composition graph that does not assemble, a component no root reaches, and a listener serving a service of another audience.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --graph`
- library [`pkg/graphkit`](../pkg/graphkit)

<a id="config-hygiene-lints"></a>

### Config read the typed way

Lint rejects a component reading `os.Getenv` or a frontend reading `process.env`, and flags a config message no binary or frontend ever loads.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- lint lane `forge lint --conventions`
- command `forge lint --config-reach`

<a id="marker-typo-lints"></a>

### Misspelled markers are caught

A misspelled `forge:` marker in Go, proto or a SQL comment, or a `(forge.v1.*)` option this binary does not define, fails lint instead of being silently inert.

_Enforced · Forge, on any infrastructure_

**Why:** `forge project capabilities` lists every marker and where it goes, in four families: entity and write policy (`entity`, `soft-delete`, `append-only`, `read-only`, `computed`, `generated`, `guards`, `immutable`, `version`, `fill`, `owner`, `secret`, `readable`, `closed-set`, `ref`, `go-type`, `identity-subject`), rpc behaviour (`custom`, `mutation`, `query-scope`, `unmanaged`), composition (`constructor`, `service`, `contract`, `no-observe`, `exclude-contract`, `external-component`, `not-a-component`, `root`, `root-only`, `standby`, `audience`, `optional-dep`, `tx-opener`, `resolves-caller`) and lint control (`allow`, `auth-unscoped-ok`, `optional-checked`), plus the `forge:lint-disable-*` suppression comments.

**Evidence:**

- command `forge lint --go-markers`
- command `forge lint --proto-markers`
- command `forge lint --column-markers`
- command `forge lint --proto-options`
- skill `proto` (`forge skill load proto`)
- command `forge project capabilities`

<a id="vendored-proto-drift"></a>

### Vendored proto drift detection

A vendored `forge.proto` that drifted from the binary's copy fails lint instead of compiling annotations nothing reads.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --vendored-protos`

<a id="proto-breaking-gate"></a>

### Breaking API changes gated

A generated `proto-breaking.yml` runs `buf breaking`, so a change that would break existing clients fails CI, and the `proto-breaking` skill carries the reserved-tag discipline.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- skill `proto-breaking` (`forge skill load proto-breaking`)

<a id="open-signup-lint"></a>

### Open signup is deliberate

A bare `forge lint` flags a login broker that mounts open registration unconditionally, so outside development an unverified address never gets a signed-in account by accident.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint`

<a id="secrets-model-lint"></a>

### Secrets wiring verified per env

`forge lint --secrets` checks every env's secrets model: each key a workload reads is produced by exactly one declaration, and an ExternalSecret never deletes its Secret on a store outage.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --secrets`

<a id="static-export-lint"></a>

### Static-export breakage caught early

For a statically hosted Next.js app, `forge lint --static-export` names each route, server action, `cookies()` call or `next/image` use the export would refuse, with file and line.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --static-export`
- source [`internal/linter/staticexport`](../internal/linter/staticexport)
- skill `frontend/serving` (`forge skill load frontend/serving`)

<a id="frontend-convention-lints"></a>

### Frontend conventions enforced

Lint keeps server data out of Zustand stores and forge-owned variables out of committed dotenv files, so React Query stays the one source of truth and a mock build never ships.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --frontend-stores`
- lint lane `forge lint --frontend-stores`
- lint lane `forge lint --conventions`

<a id="lint-rules-data-integrity"></a>

### Data-integrity rules

A column nothing writes, a filter that no-ops or a CHECK drifting from Go fails lint instead of shipping silent wrong data.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)

| Rule                                   | Bug class it prevents                                                                                                                                                                           | Lane                   |
| -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------- |
| `forgeconv-read-only-field-unwritten`  | A `forge:read-only` column nothing populates ships as the type's zero (a `$0.00` total) with no error anywhere; no constraint, test or log catches it                                           | `--read-only-fields`   |
| `forgeconv-computed-field-unwritten`   | Same silent zero for a `forge:computed` field no non-generated Go file assigns; the consequence text is derived from the field's type                                                           | `--computed-fields`    |
| `forgeconv-guarded-field-written`      | A scaffolded edit page whose `update_mask` still names a `forge:guards` column writes it raw and bypasses the rpc that owns it                                                                  | `--guarded-fields`     |
| `forgeconv-create-request-nullability` | An entity field and its `Create<Entity>Request` field disagree on `optional`, silently dropping write presence                                                                                  | `--create-nullability` |
| `forgeconv-list-filter-optional`       | A List filter without `optional` has no presence, so "no filter" collapses into "filter by zero" and generated filters no-op                                                                    | `--conventions`        |
| `forgeconv-crud-owner-resolver`        | An owner-scoping wrapper takes its owner from a different principal than its siblings (a `tenant_id` column scoped by a user id): compiles, runs, returns nothing or someone else's rows        | `--conventions`        |
| `forge-column-markers`                 | A misspelled or misplaced `forge:` marker in a `COMMENT ON` statement does nothing and postgres stores it verbatim (`forge:immutible`)                                                          | `--column-markers`     |
| `forge-identity-subject-unreadable`    | A `forge:identity-subject` column the read-only role cannot read, so the console never draws the identity link                                                                                  | `--column-markers`     |
| `forge-closed-set`                     | A CHECK whose value list drifts from the Go set it mirrors: a value Go grants and the CHECK refuses fails every write of it; the finding carries the forward migration                          | `--closed-sets`        |
| `forge-db-grants-undecided`            | A credential-shaped column (`token`, `password`, `key`, `hash`) that nobody decided is readable by the read-only `forge_ro` role                                                                | `--db-grants`          |
| `forge-db-grants-unverified`           | A credential-shaped column (`token`, `password`, `key`, `hash`) that nobody decided is readable by the read-only `forge_ro` role                                                                | `--db-grants`          |
| `forge-fixture-rejected`               | A scaffolded CRUD test fixture the current schema rejects (a GENERATED column, a UNIQUE clash, a dangling FK, a later CHECK), found by running each statement in a rolled-back transaction      | `--fixture-drift`      |
| `forge-fixture-unverified`             | A scaffolded CRUD test fixture the current schema rejects (a GENERATED column, a UNIQUE clash, a dangling FK, a later CHECK), found by running each statement in a rolled-back transaction      | `--fixture-drift`      |
| `forge-time-bucket-session-tz`         | A two-argument `date_trunc` over a `TIMESTAMPTZ` truncates in the session timezone, so every daily total moves with the deploy host                                                             | `--time-bucketing`     |
| `forgeconv-bun-dollar-placeholder`     | `$1`-style placeholders ported into bun-formatted SQL, which bun does not bind, so the statement fails at runtime                                                                               | `--conventions`        |
| `forgeconv-bun-literal-question-mark`  | A literal `?` in bun-formatted SQL, which bun consumes as a placeholder, so the statement runs against shifted arguments                                                                        | `--conventions`        |
| `forgeconv-tx-lock-under-serializable` | A `SELECT … FOR UPDATE` or advisory lock inside a SERIALIZABLE or REPEATABLE READ transaction: the snapshot predates the wait, so each contended wait costs a full rollback or drops the update | `--conventions`        |
| `forgeconv-service-dir-consistency`    | A proto service name that disagrees with its `proto/services/` directory, so scaffolds and handler directories land where nothing expects them                                                  | `--conventions`        |

<a id="lint-rules-composition-deps"></a>

### Architecture stays testable

Lint keeps Deps interface-typed and narrow, every component reachable and audience-correct, so services stay mockable and wired.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)

| Rule                                          | Bug class it prevents                                                                                                                                                                                                                  | Lane                    |
| --------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `forgeconv-internal-package-contract-names`   | A non-canonical `contract.go` (`Service`, `Deps`, `New(Deps) Service`): the generated bootstrap hard-codes those names and would not compile. `forgeconv-contract-names` in an old `lint.rules` still grades it, with a rename warning | `--contract`            |
| `forgeconv-internal-package-missing-contract` | A component built by hand without `contract.go` is invisible to wiring, mocks, the observability decorator and every reporter                                                                                                          | `--contract`            |
| `forgeconv-deps-are-interfaces`               | A concrete Deps field defeats the all-mock unit-test surface `New(Deps)` exists for                                                                                                                                                    | `--contract`            |
| `forgeconv-outbound-io-no-rpc`                | A package marked `// forge:outbound-io` that also serves an inbound handler contradicts its own marker                                                                                                                                 | `--contract`            |
| `forgeconv-deps-surface`                      | A Deps interface declaring methods the package never uses: the abstraction is wider than the need, so it leaks (autofix: `forge fix narrow-deps`)                                                                                      | `--conventions`         |
| `forgeconv-no-container-deps`                 | A Deps field that is a container (the composition root's types, a locator, `any`): the dependency graph hides in a bag                                                                                                                 | `--conventions`         |
| `forgeconv-deps-no-downcast`                  | A type assertion turning a Deps field back into a concrete type                                                                                                                                                                        | `--conventions`         |
| `forgeconv-no-getenv`                         | A component reading the process environment itself instead of its typed config                                                                                                                                                         | `--conventions`         |
| `forgeconv-deps-root-config`                  | A Deps field holding the whole root config instead of the component's own block                                                                                                                                                        | `--conventions`         |
| `forgeconv-optional-dep-marker-position`      | A `// forge:optional-dep` that is misplaced is silently ignored, so `validateDeps` still gates a field the author thinks is optional                                                                                                   | `--conventions`         |
| `forge-optional-deps-guard`                   | An unguarded dereference of an optional dep is a latent nil-pointer panic no construction-time gate can catch                                                                                                                          | `--optional-deps-guard` |
| `forge-config-deps`                           | A scalar Deps field is configuration, not a collaborator; wiring leaves a typed zero and a TODO forever                                                                                                                                | `--config-deps`         |
| `forge-config-unreachable`                    | A config message no binary and no frontend loads still generates Go, KCL and env projections and is read by nobody                                                                                                                     | `--config-reach`        |
| `forgeconv-config-service-name`               | A config field bound to `OTEL_SERVICE_NAME` overrides the per-workload name project-wide, so every workload files telemetry as service `unknown`                                                                                       | `--conventions`         |
| `forge-graph-unreachable`                     | A component or provider no root reaches: no process constructs it                                                                                                                                                                      | `--graph`               |
| `forge-graph-audience`                        | A listener serving a service through a chain authenticated by another audience: a daemon caller could reach a user service                                                                                                             | `--graph`               |
| `forge-graph-assemble`                        | A composition graph that does not assemble (the error `generate` would fail with)                                                                                                                                                      | `--graph`               |
| `forgeconv-allocate-port-spacing`             | Two `allocate_port` bases congruent mod the block size collide at some worktree number, so two listeners bind one port once the stack ceiling is raised                                                                                | `--conventions`         |
| `component-drift/orphan-entry`                | A `workloads.k` entry with no component in the code behind it                                                                                                                                                                          | bare `forge lint`       |
| `component-drift/undeclared-component`        | A component in the code with no `workloads.k` entry, so nothing deploys it                                                                                                                                                             | bare `forge lint`       |
| `component-drift/no-such-command`             | A workload whose args select a command the binary does not have                                                                                                                                                                        | bare `forge lint`       |
| `forgeconv-handler-file-size`                 | One handler file grown past `lint.handler_file_max_loc`; split per rpc with `forge scaffold handler-file`                                                                                                                              | `--conventions`         |

<a id="lint-rules-api-errors"></a>

### API and error-handling rules

Lint stops internal error text leaking to callers, misspelled markers going inert and holes in activity history.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)

| Rule                                 | Bug class it prevents                                                                                                                                                                                                  | Lane                |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| `forgeconv-handler-connect-error`    | `connect.NewError(code, err)` publishes `err.Error()` (a driver's SQL, a DSN, an internal host) to the caller and pins a guessed code over a classified one (autofix: `forge fix connect-errors`)                      | `--conventions`     |
| `forgeconv-unwrapped-domain-error`   | A handler returning a collaborator's error unwrapped puts a domain error on the wire as `500 unknown` and drops the reason header                                                                                      | `--conventions`     |
| `forgeconv-no-handler-error-mapping` | A hand-rolled `toConnectError` switch re-created per service                                                                                                                                                           | `--conventions`     |
| `forgeconv-one-service-per-file`     | A multi-service proto file                                                                                                                                                                                             | `--conventions`     |
| `forgeconv-activity-declared`        | A mutating rpc that does not declare its activity event (or `activity: { none: "<why>" }`) leaves a hole in a user's history; judged from the compiled descriptors, so it cannot disagree with the runtime interceptor | `--conventions`     |
| `forge-proto-markers`                | A misspelled `// forge:` marker in a `.proto` comment is inert and warns nowhere                                                                                                                                       | `--proto-markers`   |
| `forge-proto-options`                | A `(forge.v1.*)` annotation naming a field this binary does not define compiles under buf and is read by nothing                                                                                                       | `--proto-options`   |
| `forge-vendored-proto-drift`         | A vendored `forge.proto` that diverged from the binary's copy: annotations compile, then are ignored                                                                                                                   | `--vendored-protos` |
| `forge-go-markers`                   | A `//forge:` comment in Go that forge does not read, an unknown name or a spelling its parser ignores                                                                                                                  | `--go-markers`      |
| `forgeconv-open-signup`              | A login broker that mounts `POST /auth/register` unconditionally: outside development an unverified address gets a signed-in account, which opens email-bound invitations to takeover (advisory)                       | bare `forge lint`   |
| `forgeconv-identity-methods`         | A `forge.ManagedIdentity` declaring a sign-in method or signup mode the project's own broker does not serve, so the declaration is converged and silently ignored                                                      | bare `forge lint`   |
| `typed-config-guardrail`             | An `os.Getenv` the typed-config forbidigo guard reports (advisory unless `--strict`)                                                                                                                                   | bare `forge lint`   |

<a id="lint-rules-tracing"></a>

### Traces that never break

`forge lint --tracing` names every place a request's trace is cut (a log call without its context, a hand-built HTTP client, an untraced database pool, an uninstrumented contract) and `--fix` rewrites most of them.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In a Go module, run `forge lint --tracing`; `--fix` rewrites each log call to its `…Context` form. **Agent time:** ~10 min.

**Why:** All four rules are AST passes over owned source that err toward silence where the code may already be right; a logging wrapper of your own joins through `lint.tracing.log_wrappers`.

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)
- command `forge lint --tracing`
- source [`internal/linter/tracinglint`](../internal/linter/tracinglint)
- skill `observability/tracing-lint` (`forge skill load observability/tracing-lint`)

| Rule                                    | Bug class it prevents                                                                                                                                                            | Lane        |
| --------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| `forge-tracing-log-without-ctx`         | A log call made where a context is in scope but not passed has no `trace_id` and cannot be joined to anything (autofixed to the `…Context` form by `forge lint --tracing --fix`) | `--tracing` |
| `forge-tracing-raw-http-client`         | A hand-built `*http.Client` or `http.Get` cuts the trace at the call; use `observe.NewHTTPClient`                                                                                | `--tracing` |
| `forge-tracing-raw-sql-open`            | `sql.Open`/`pgxpool.New` with no tracer drops every query span; use `dbtrace`                                                                                                    | `--tracing` |
| `forge-tracing-contract-uninstrumented` | A contract's `New(…)` used without its generated decorator (autofixed to `NewInstrumented`)                                                                                      | `--tracing` |

<a id="lint-rules-tests"></a>

### Test-hygiene rules

Unmarked slow tests, stale cached passes and hook tests that can never pass are caught before merge.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)

| Rule                                 | Bug class it prevents                                                                                                                                                                                                                                                                      | Lane            |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------- |
| `forge-slow-test-unmarked`           | A test that execs `go`, `forge`, `kcl`, `node`, `npm`, `git` or `docker`, drives containers or dials an external host without `testkit.Slow` runs in the blocking `-short` tier, and a file its child process reads is invisible to Go's test cache, so a stale PASS replays after an edit | `--slow-tests`  |
| `forge-slow-test-inputs`             | A test that execs `go`, `forge`, `kcl`, `node`, `npm`, `git` or `docker`, drives containers or dials an external host without `testkit.Slow` runs in the blocking `-short` tier, and a file its child process reads is invisible to Go's test cache, so a stale PASS replays after an edit | `--slow-tests`  |
| `forge-test-fixed-sleep`             | A test that sleeps a fixed time and then asserts, guessing how long the work takes: on a loaded machine the guess is short and the test flakes (`testkit.Eventually` / `testkit.Receive` wait for the event)                                                                               | `--test-waits`  |
| `forge-test-tight-deadline`          | An event wait that fails the test after a deadline under 5s (a select on `time.After`, testify `Eventually`, a poll bounded by `time.Now().Add`), which a busy machine overruns                                                                                                            | `--test-waits`  |
| `forge-test-wall-clock-bound`        | A test that fails when real elapsed time passes a bound under 5s, measuring how busy the machine was rather than what the code did                                                                                                                                                         | `--test-waits`  |
| `forgeconv-handler-tests-use-tdd`    | Hand-rolled `{name, call}` tables instead of `tdd.RunRPCCases` (codemod: `forge project migrate tdd`)                                                                                                                                                                                      | `--tests`       |
| `forgeconv-frontend-hook-tests`      | A generated hooks file with no sibling test                                                                                                                                                                                                                                                | `--tests`       |
| `forgeconv-frontend-hook-test-shape` | A hook test whose assertions can never pass (a query-shaped test on a mutation hook, or the reverse)                                                                                                                                                                                       | `--conventions` |

<a id="lint-rules-frontends"></a>

### Frontend build rules

What a static export would refuse, build-time env inlining and lockfile drift fail with file:line.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)

| Rule                                       | Bug class it prevents                                                                                                                                                                                      | Lane                |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| `static-export-not-exported`               | A frontend bound to a static runtime whose build is not a static export (`output: "export"`), so there is nothing to publish                                                                               | `--static-export`   |
| `static-export-dynamic-segment`            | A dynamic route with no `generateStaticParams`, which `next build` refuses because an export renders every page at build time                                                                              | `--static-export`   |
| `static-export-route-handler`              | A route-handler GET not declared static, which `next build` refuses, or another method the export drops so a static host answers it with 404                                                               | `--static-export`   |
| `static-export-middleware`                 | Middleware that runs only under `next dev`: a static host has no server, so whatever it guards, rewrites or redirects is unguarded in production                                                           | `--static-export`   |
| `static-export-server-action`              | A `'use server'` Server Action, which needs the Next.js server a static export does not have                                                                                                               | `--static-export`   |
| `static-export-next-headers`               | `cookies()`, `headers()` or `draftMode()` from `next/headers`, which read a request a static export never has                                                                                              | `--static-export`   |
| `static-export-dynamic-config`             | `export const dynamic = "force-dynamic"` or `revalidate = 0`, which ask for per-request rendering an export cannot do                                                                                      | `--static-export`   |
| `static-export-next-image`                 | `next/image` with its default server-side optimizer, which the export refuses unless `images.unoptimized` is set                                                                                           | `--static-export`   |
| `static-export-config-routes`              | `rewrites`, `redirects` or `headers` in `next.config` not gated to development: they work under `next dev` and 404 or vanish on a static host                                                              | `--static-export`   |
| `static-export-trailing-slash`             | A frontend on a bucket with `trailingSlash` off: the export writes `<route>.html` and a bucket serves objects by exact name, so every route but `/` 404s                                                   | `--static-export`   |
| `forgeconv-frontend-process-env`           | Reading `process.env` or `import.meta.env` in frontend source freezes the bundle to the environment it was built for, defeating build-once promotion, and bypasses the typed schema and the secret refusal | `--conventions`     |
| `forgeconv-frontend-env-forge-owned`       | A committed dotenv line setting a forge-owned variable (mock API, API URL) drifts from KCL and risks shipping a mock build                                                                                 | `--conventions`     |
| `forgeconv-frontend-stores-no-server-data` | A Zustand store importing a generated Connect client creates a second source of truth beside React Query                                                                                                   | `--frontend-stores` |
| `forge-frontend-lock-drift`                | A `package.json` range the lockfile does not record: the state `npm ci` and `pnpm --frozen-lockfile` refuse                                                                                                | bare `forge lint`   |

<a id="lint-rules-deploy-secrets"></a>

### Deploy and secrets rules

Undrainable PDBs, wrong-arch builds, reconcile-breaking Flux Jobs and missing secret keys are caught from each env's real render.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)

| Rule                                   | Bug class it prevents                                                                                                                                                            | Lane              |
| -------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------- |
| `pdb-blocks-drain`                     | A PodDisruptionBudget that can never allow a disruption blocks every node drain forever (checked over each env's real render, hand-written manifests included)                   | bare `forge lint` |
| `hosted-image-base/host-bearing-image` | An image naming a registry host on a hosted workload, where the platform admits only its own subtree                                                                             | bare `forge lint` |
| `flux-job-ttl`                         | A Job with a TTL, which Flux re-creates every interval after the TTL deletes it, so it runs again and again                                                                      | bare `forge lint` |
| `flux-job-unhashed`                    | A Job whose name carries no spec hash, so every reconcile after its first change fails on the immutable Job                                                                      | bare `forge lint` |
| `flux-force-on-data`                   | A force-recreate annotation on a kind that holds data, so a reconcile can delete and re-create it                                                                                | bare `forge lint` |
| `identity-share-with`                  | A `ManagedIdentity.share_with` one env's render cannot judge: a missing or chained owner env                                                                                     | bare `forge lint` |
| `shellbuild-tokens/retired-token`      | A retired `${TARGETARCH}` token left in a ShellBuild command: the shell resolves it empty, `GOARCH=` builds for the host, and an amd64 cluster silently receives an arm64 binary | bare `forge lint` |
| `no-dotenv`                            | A `.env` file: injected wholesale into host services so values never have to be declared in KCL, which is how config stops being reproducible                                    | bare `forge lint` |
| `secrets-cluster-kind`                 | A cluster-scoped ESO kind, `PushSecret` or generator the operator forge installs has disabled: inert at best, a cluster-wide credential path at worst                            | `--secrets`       |
| `secrets-es-policy`                    | An ExternalSecret that would delete its Secret on a store outage, or polls its store faster than the cluster's admission policy allows                                           | `--secrets`       |
| `secrets-key-coverage`                 | A pod stuck in `CreateContainerConfigError` because no declaration produces a key it reads                                                                                       | `--secrets`       |
| `secrets-unreferenced-key`             | A declared Secret or key no workload of the env reads (advisory: a controller or another env may still read it)                                                                  | `--secrets`       |
| `secrets-immutable-pin`                | A floating read of an immutable store name, so rotating the value in the store silently changes what the pod gets                                                                | `--secrets`       |
| `secrets-out-of-band-reason`           | An out-of-band Secret whose reason restates the fact or is a placeholder instead of saying why it is not synced from the store                                                   | `--secrets`       |
| `secrets-single-owner`                 | A Secret with two owners that fight over its keys                                                                                                                                | `--secrets`       |
| `openbao-wildcard-read`                | An OpenBao policy that reads a path glob with no stated reason                                                                                                                   | `--secrets`       |
| `secrets-escrow-in-cluster`            | An OpenBao whose seal key and init output are escrowed in the cluster they protect, so losing the cluster loses every backup of the store                                        | `--secrets`       |
| `secrets-env-not-rendered`             | An env whose render fails, so its secrets could not be judged (a warning; `forge env render` reports the render error itself)                                                    | `--secrets`       |

<a id="lint-rules-scaffold-ownership"></a>

### Generated code stays honest

Hand-edited generated files, ignored generated code and committed machine-local state fail lint.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/lint/lint_rules.go`](../internal/cli/lint/lint_rules.go)
- source [`internal/linter/scaffolds`](../internal/linter/scaffolds)
- command `forge lint --generated-drift`

| Rule                              | Bug class it prevents                                                                                                                                         | Lane                  |
| --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------- |
| `forge-generated-file-edited`     | A `Code generated … DO NOT EDIT` file hand-edited after forge wrote it, which the next `generate` silently overwrites                                         | `--generated-drift`   |
| `forge-generated-file-unverified` | A `Code generated … DO NOT EDIT` file hand-edited after forge wrote it, which the next `generate` silently overwrites                                         | `--generated-drift`   |
| `generated-code-ignored`          | A `.gitignore` that ignores generated code, so CI fails on `undefined:` symbols that live in ignored files while `verify-generated` is blind to ignored paths | bare `forge lint`     |
| `machine-local-state-tracked`     | A machine-local file committed to the repository                                                                                                              | bare `forge lint`     |
| `tool-derived-file-tracked`       | A tool-only generated output committed to the repository                                                                                                      | bare `forge lint`     |
| `forge-state-ignored`             | A `.forge/` ignore line that voids the scaffold's re-includes, so deleted scaffolds come back in every clone                                                  | bare `forge lint`     |
| `scaffold-not-customized`         | A file still carrying `FORGE_SCAFFOLD` placeholders (a warning: an uncustomized scaffold is pending work, not a defect)                                       | bare `forge lint`     |
| `gen-missing-header`              | A `_gen` file that does not claim to be generated, so nothing marks it as forge's to overwrite                                                                | bare `forge lint`     |
| `gen-missing-source`              | A `_gen` file that names no input, so nobody can tell what regenerates it                                                                                     | bare `forge lint`     |
| `workaround-testing-extras`       | A `testing_extras.go` papering over a missing test primitive                                                                                                  | `--check-workarounds` |
| `workaround-cmd-not-in-binaries`  | A `cmd/<name>.go` binary the project never declared                                                                                                           | `--check-workarounds` |
| `golangci-build-tags`             | A finding in a file behind a project build tag (`//go:build integration`), which golangci-lint, run once with no tags, never saw                              | bare `forge lint`     |

<a id="lint-rules-forge-internal"></a>

### Forge's own template checks

Forge lints its own templates for the ownership banner each tier must carry and for symbols a regenerated template no longer defines, so a forge release never scaffolds a project that fails to build.

_Enforced · Forge, on any infrastructure_

**Why:** These rules guard forge's own source tree and are a no-op in a project; they are gradable, so they are listed rather than left out. `--scaffolds`, `--banners`, `--check-workarounds`, `--exported-vars` and the `--suggest-*` helpers are the hidden maintainer lanes.

**Evidence:**

- lint lane `forge lint --banners`
- lint lane `forge lint --scaffolds`
- source [`internal/linter/scaffolds`](../internal/linter/scaffolds)

| Rule                                    | Bug class it prevents                                                                                                                                                              | Lane          |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- |
| `cross-tier-derived-symbol`             | Forge-internal: a template emitting a reference to a per-component symbol no regenerated template defines (the `c.WorkerNightly undefined` build break after a component is added) | `--scaffolds` |
| `banner-tier1-missing-generated-header` | Forge-internal: a template that does not carry its ownership tier's banner                                                                                                         | `--banners`   |
| `banner-tier2-missing-scaffold-header`  | Forge-internal: a template that does not carry its ownership tier's banner                                                                                                         | `--banners`   |
| `banner-unclassified`                   | Forge-internal: a template that does not carry its ownership tier's banner                                                                                                         | `--banners`   |

---

<a id="security"></a>

## Security

Production-grade defaults on the first commit: auth that fails closed, errors that never leak internals, secrets that never sit in a `.env` file, and scanned dependencies. Authentication is a middleware: an interceptor validates the credential and puts the claims on the context. Forge authenticates; it does not authorize, and says so: what a caller may do is the handler's logic.

<a id="auth-fail-closed"></a>

### Auth that fails closed

Every rpc needs a valid credential unless its proto says `auth_required: false`, so a forgotten annotation never publishes an endpoint and there is no second allow-list to drift.

_Enforced · Forge, on any infrastructure_

**Use:** `option (forge.v1.method) = { auth_required: false }` publishes exactly one rpc.

**Why:** The setting is projected into `pkg/middleware/procedures_gen.go`, so there is one source.

**Evidence:**

- source [`pkg/authn/authn.go`](../pkg/authn/authn.go)
- source [`internal/assets/proto/forge/v1/forge.proto`](../internal/assets/proto/forge/v1/forge.proto)
- command `forge project audit`
- skill `auth` (`forge skill load auth`)

<a id="svcerr-redaction"></a>

### Errors never leak internals

A handler returns `svcerr.Wrap(err)` and the caller gets a classified Connect code while a driver's SQL, a DSN or an internal host stays in the server's log, and the fault class (server, user, canceled) sets the log level.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In a Go Connect service, `go get github.com/reliant-labs/forge`, add `svcerr.Interceptor()` and return `svcerr.Wrap(err)` from handlers. **Agent time:** ~30 min.

**Use:** `return nil, svcerr.Wrap(err)` from every rpc (`svcerr.WithReason`, `svcerr.WithClass`); `forge fix connect-errors` rewrites hand-built Connect errors.

**Why:** `svcerr.Classify` sets each record's `error_class` (`server`, `user`, `canceled`), so ERROR means "we must act" and alerts, forwarders and post-deploy checks can trust it.

**Evidence:**

- library [`pkg/svcerr`](../pkg/svcerr)
- command `forge fix connect-errors`
- lint lane `forge lint --conventions`
- source [`pkg/svcerr/interceptor.go`](../pkg/svcerr/interceptor.go)
- skill `observability` (`forge skill load observability`)

<a id="secrets-model"></a>

### Secrets never in git

A secret is declared once as a reference and bound per env to a provider, so rendered KCL only ever carries references, and `forge secret set` requires the env so a value cannot land in the wrong one.

_Enforced · Forge, on any infrastructure_

**Use:** A `sensitive` config field or `EnvVar.secret_ref`; `forge secret set --env <env> <KEY>`.

**Evidence:**

- command `forge secret set`
- library [`pkg/secretaccess`](../pkg/secretaccess)
- skill `secrets` (`forge skill load secrets`)

<a id="vuln-scan"></a>

### Dependency and image scanning

`forge ci vuln-scan` runs govulncheck, npm audit and image scans per `forge.yaml`, refuses a pass it did not verify, and takes reviewed, expiring exemptions instead of silent ignores.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge ci vuln-scan`
- source [`internal/cli/ci_vuln_npm.go`](../internal/cli/ci_vuln_npm.go)
- source [`internal/cli/ci_vuln_exempt.go`](../internal/cli/ci_vuln_exempt.go)

<a id="image-supply-chain"></a>

### Signed images, SBOM and provenance

The generated `build-images.yml` attaches an SPDX SBOM, a keyless cosign signature and a build-provenance attestation to every image, bound to its digest.

_Enforced · Forge, on any infrastructure_

**Why:** This is the generated GitHub workflow only; `forge env build` itself does not sign, and the DeliveryPolicy `provenance_sbom`/`trusted_builder`/`admission_verify` controls are recorded, not enforced.

**Evidence:**

- source [`internal/templates/ci/github/build-images.yml.tmpl`](../internal/templates/ci/github/build-images.yml.tmpl)

<a id="managed-identity"></a>

### Managed identity, declared once

Users, roles, owners, signup mode and sign-in methods are declared once in `deploy/kcl/identity.k`, and forge fills the issuer, audience and broker credentials per env: a dev IdP locally, the control plane's identity instance in staging and prod.

_Enforced · Forge, on any infrastructure_

**Use:** `forge.ManagedIdentity` in `deploy/kcl/identity.k`.

**Why:** A workload that sets `JWT_ISSUER`, `JWT_AUDIENCE`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI` or `IDP_BASE` beside `Bundle.identity` is refused at render, so each field has one source.

**Evidence:**

- source [`internal/templates/deploy/kcl/identity.k.tmpl`](../internal/templates/deploy/kcl/identity.k.tmpl)
- library [`pkg/auth`](../pkg/auth)
- library [`pkg/identity`](../pkg/identity)
- skill `auth` (`forge skill load auth`)
- skill `deploy/managed-identity` (`forge skill load deploy/managed-identity`)

<a id="local-dev-idp"></a>

### Real sign-in locally, no setup

`forge env up` converges a checksum-verified Zitadel as a host process and a local login broker, so the sign-in you test in dev is the one production runs — no throwaway auth shim.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env up dev`

**Why:** This removes the usual detour through a hand-rolled credential-injecting shim that has to be deleted later. Mail goes to a declared Mailpit.

**Evidence:**

- command `forge env up`
- library [`pkg/devidp`](../pkg/devidp)
- skill `auth/dev-loop` (`forge skill load auth/dev-loop`)

<a id="login-broker-methods"></a>

### Passwords, passkeys, magic links, social

The scaffolded login broker serves passwords (with a complexity policy), passkeys, magic links and social sign-in as declared in KCL, and lint fails a method the broker does not serve.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`pkg/devidp/passkey.go`](../pkg/devidp/passkey.go)
- source [`pkg/devidp/magic.go`](../pkg/devidp/magic.go)
- source [`pkg/devidp/social.go`](../pkg/devidp/social.go)
- source [`pkg/devidp/password_policy.go`](../pkg/devidp/password_policy.go)
- source [`internal/cli/lint/lint_identity_methods.go`](../internal/cli/lint/lint_identity_methods.go)
- source [`internal/cli/lint/lint_open_signup.go`](../internal/cli/lint/lint_open_signup.go)
- skill `auth/frontend` (`forge skill load auth/frontend`)

<a id="hosted-identity-admin"></a>

### User admin from the CLI

`forge cloud identity` lists, invites, resets, locks, re-roles, signs out, exports and deletes a hosted environment's users, and shows the state of its identity instance.

_Enforced · Reliant hosting_

**Use:** `forge cloud identity users|invite|reset|lock|unlock|set-roles|revoke-sessions|export|delete`

**Evidence:**

- command `forge cloud identity users`
- command `forge cloud identity status`
- `internal/identityapi` in the control-plane repo

<a id="unscoped-auth-audit"></a>

### Find rpcs that ignore the caller

`forge project audit` includes an `unscoped_auth` list of authenticated rpcs that never resolve who is calling, with `// forge:auth-unscoped-ok: <reason>` as the only acknowledged exception.

_Enforced · Forge, on any infrastructure_

**Use:** `forge project audit --json`

**Evidence:**

- command `forge project audit`
- marker `// forge:auth-unscoped-ok`

<a id="audience-isolation"></a>

### One listener per caller type

`// forge:audience` names each authenticator's trust domain and `forge lint --graph` fails a listener serving another audience, so a daemon token cannot reach a user service.

_Enforced · Forge, on any infrastructure_

**Use:** `// forge:audience <word>` on an authenticator.

**Evidence:**

- marker `// forge:audience`
- command `forge lint --graph`
- skill `architecture/graph-checks` (`forge skill load architecture/graph-checks`)

<a id="principal-chokepoint"></a>

### Owner scoping at one chokepoint

Claims are enriched by stable subject (never email) before any handler runs, and a `forge:owner` column makes forge scaffold the owner-scoping wrapper that lint keeps consistent across siblings.

_Enforced · Forge, on any infrastructure_

**Why:** `enrichClaims` runs after validation and before any handler, hydrating by the stable subject. Binding an invitation or membership by email must require a verified email.

**Evidence:**

- marker `// forge:owner`
- lint lane `forge lint --conventions`
- skill `auth/authorization` (`forge skill load auth/authorization`)

<a id="api-keys"></a>

### API keys never pass as tokens

API keys are routed by shape to your own table, hashed and compared in constant time, and the credential kind is stamped by the authenticator so a token can't impersonate a key.

_Enforced · Forge, on any infrastructure_

**Why:** `X-API-Key` or `Bearer fk_…` is routed by shape to a validator over your own table, and the authenticator stamps `Claims.Credential` itself, so a per-tenant key can carry its tenant and role and a handler can refuse key management made with a key.

**Evidence:**

- library [`pkg/apikey`](../pkg/apikey)
- source [`pkg/auth/credential.go`](../pkg/auth/credential.go)
- library [`pkg/auth`](../pkg/auth)
- library [`pkg/authn`](../pkg/authn)
- library [`pkg/oauth2`](../pkg/oauth2)
- skill `auth/api-keys` (`forge skill load auth/api-keys`)

<a id="machine-tokens"></a>

### Scoped machine access tokens

`pkg/accesstoken` gives one machine credential with a closed scope vocabulary that can never grant authority its holder lacks; a person mints the CI token in the web app (Settings → API tokens), and `forge cloud token` manages them where the control plane admits a CLI login.

_Enforced · Forge, on any infrastructure_

**Why:** The token is the `rlat_` wire format, hashed and compared in constant time.

**Evidence:**

- library [`pkg/accesstoken`](../pkg/accesstoken)
- command `forge cloud token create`

<a id="jwks-outage-survival"></a>

### Restarts survive an IdP outage

A JWKS snapshot lets a service restart and keep validating tokens while its issuer is down.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`pkg/auth/jwks_snapshot.go`](../pkg/auth/jwks_snapshot.go)
- skill `auth/shared-issuer` (`forge skill load auth/shared-issuer`)

<a id="byo-issuer"></a>

### Bring your own IdP

Declare no identity in an env and set `jwt_issuer` and `oidc_client_id` in its config, and Auth0, Okta, Keycloak or any OIDC issuer plugs in.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`pkg/auth/providers.go`](../pkg/auth/providers.go)
- source [`pkg/auth/multi.go`](../pkg/auth/multi.go)

<a id="edge-hygiene"></a>

### Security headers and request limits

Security headers, CORS, Connect payload caps and request-id middleware ship in the chain, and `forge doctor` checks caps, probes and resource limits of every env with nothing running.

_Enforced · Forge, on any infrastructure_

**Use:** `forge doctor`

**Evidence:**

- source [`pkg/middleware/security_headers.go`](../pkg/middleware/security_headers.go)
- source [`pkg/middleware/cors.go`](../pkg/middleware/cors.go)
- source [`pkg/middleware/httpstack.go`](../pkg/middleware/httpstack.go)
- command `forge doctor`

<a id="middleware-hardening"></a>

### Rate limits, idempotency, webhook checks

Ready-made middleware for rate limits, idempotency keys, webhook signature checks, log redaction and per-request audit records ships in `pkg/middleware`.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`pkg/middleware/ratelimit.go`](../pkg/middleware/ratelimit.go)
- source [`pkg/middleware/idempotency.go`](../pkg/middleware/idempotency.go)
- source [`pkg/middleware/webhook.go`](../pkg/middleware/webhook.go)
- source [`pkg/middleware/redact.go`](../pkg/middleware/redact.go)
- source [`pkg/middleware/audit.go`](../pkg/middleware/audit.go)

<a id="ssrf-guard"></a>

### Outbound SSRF guard

`pkg/netguard` refuses outbound connections to loopback, private, link-local, cloud-metadata and in-cluster destinations, removing the SSRF review from every fetch-by-URL.

_Enforced · Forge, on any infrastructure_

**Use:** `forge project libraries netguard`

**Evidence:**

- source [`pkg/netguard/netguard.go`](../pkg/netguard/netguard.go)
- source [`pkg/netguard/ranges.go`](../pkg/netguard/ranges.go)
- library [`pkg/netguard`](../pkg/netguard)

<a id="secret-providers"></a>

### Pluggable secret providers per env

`forge.FileSecrets` (a gitignored file store) for dev, `forge.HostedSecrets` (write-only) for hosted envs, `forge.ExternalSecrets` and the `forge.ExternalSecretsOperator` component for a cluster, a self-run `forge.OpenBao` with JWT-only client auth, and `forge.ControlPlaneSecret` for a value the control plane renews with no restart.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge secret allow`
- command `forge secret access`
- source [`pkg/deploy/openbao`](../pkg/deploy/openbao)
- source [`pkg/deploy/eso`](../pkg/deploy/eso)
- skill `secrets` (`forge skill load secrets`)
- skill `deploy/external-secrets` (`forge skill load deploy/external-secrets`)
- skill `deploy/openbao` (`forge skill load deploy/openbao`)
- skill `deploy/control-plane-secret` (`forge skill load deploy/control-plane-secret`)

<a id="hosted-secrets"></a>

### Write-only hosted secrets

Hosted secrets are stored in an OpenBao-backed store, write-only (set once, never read back), scoped to one (org, env), with per-workflow access grants.

_Enforced · Reliant hosting_

**Use:** `forge secret set --env <env> <KEY>`; `forge secret allow|deny|access`

**Evidence:**

- command `forge secret set`
- command `forge secret allow`
- `internal/tenantsecret` in the control-plane repo
- `internal/secretstore` in the control-plane repo
- `docs/openbao-secret-store.md` in the control-plane repo

<a id="no-dotenv"></a>

### No .env files

A bare `forge lint` fails on a `.env` file, because a dotenv is injected wholesale into every host service and its values never have to be declared in KCL.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint`
- lint lane `forge lint --conventions`

<a id="secret-verify"></a>

### Verify live secrets without seeing them

`forge env secrets verify` compares a live Secret with its store copy per key — match or no-match, never a value or hash — and `adopt` hands a verified one to ESO.

_Enforced · Forge, on any infrastructure_

**Why:** `forge env shape` never carries a secret value either.

**Evidence:**

- command `forge env secrets verify`
- command `forge env secrets adopt`
- command `forge lint --secrets`
- skill `deploy/shape` (`forge skill load deploy/shape`)

<a id="secretgen"></a>

### Generated keys and certificates

`forge generate` mints random keys, passwords that meet a complexity policy, and key pairs and certificates into the file secret store.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/secretgen`](../pkg/secretgen)

<a id="key-rotation"></a>

### Encryption with key rotation

`pkg/crypto` seals values with AES-256-GCM under a versioned keyring, so keys rotate without making data unreadable.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/crypto`](../pkg/crypto)

<a id="outbound-redaction"></a>

### Credentials redacted on the way out

`pkg/redact` strips credential shapes and personal paths from text leaving the process, and `forge report-bug --dry-run` shows exactly what would be sent.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/redact`](../pkg/redact)
- command `forge report-bug`
- source [`docs/report-bug.md`](../docs/report-bug.md)

<a id="guarded-db-query"></a>

### Audited read-only prod queries

`forge db query` runs one statement in a READ ONLY transaction as a role that cannot read `forge:secret` columns, demands `--confirm-env` and a reason, and on a hosted env needs the org's `db:query` grant and records an audit row and an activity event.

_Enforced · Forge, on any infrastructure_

**Use:** `forge db query <env> "<sql>" --confirm-env <env> --reason "<why>"`

**Why:** A `forge:secret` column is refused by name and through `select *`, `upper(col)` and `row_to_json`, and the env's `data_access` policy (`open`, `audited`, `deny`) applies.

**Evidence:**

- command `forge db query`
- source [`internal/cli/db_query_audit.go`](../internal/cli/db_query_audit.go)
- library [`pkg/dbquery`](../pkg/dbquery)
- marker `// forge:secret`
- library [`pkg/dbgrants`](../pkg/dbgrants)
- library [`pkg/dbbrowse`](../pkg/dbbrowse)
- skill `db/query` (`forge skill load db/query`)

<a id="go-license-check"></a>

### Forbidden licenses fail the gate

A generated project's `forge ci gate` runs go-licenses over its Go dependencies and fails on one whose license type is forbidden or restricted; there is no license check for npm dependencies.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cigate/license.go`](../internal/cigate/license.go)
- command `forge ci gate`

<a id="signed-forge-releases"></a>

### Signed, verified forge installs

Every forge release ships checksums and a Sigstore attestation over its archives, and `install.sh` verifies them (with gh or cosign) before installing.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`.github/workflows/release-attest.yml`](../.github/workflows/release-attest.yml)
- source [`install.sh`](../install.sh)
- source [`scripts/build-release-assets.sh`](../scripts/build-release-assets.sh)

<a id="dependabot-codeowners"></a>

### Dependabot and CODEOWNERS shipped

New projects get Dependabot updates and a CODEOWNERS file out of the box.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/templates/ci/github/dependabot.yml.tmpl`](../internal/templates/ci/github/dependabot.yml.tmpl)
- source [`internal/templates/ci/github/CODEOWNERS.tmpl`](../internal/templates/ci/github/CODEOWNERS.tmpl)

<a id="signed-delivery"></a>

### Signed release delivery

Delivery bundles are signed with the org's key and the cluster applies a digest only if the signature verifies.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/deliverysign` in the control-plane repo
- `internal/workers/delivery_signer` in the control-plane repo

<a id="hosted-sandbox-isolation"></a>

### Tenant workloads sandboxed

Hosted workloads run on an isolated-pool runtime with per-(org, env) namespaces, and a cross-tenant acceptance suite proves no query, secret or network path crosses tenants.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/isolation` in the control-plane repo
- `internal/operators/workload/decorate.go` in the control-plane repo
- `internal/tenantnetpol` in the control-plane repo

<a id="managed-postgres-network-containment"></a>

### Databases fenced off by default

A hosted database gets a namespace default-deny, ingress only from your own workloads, and no internet egress (only its backup store and the API server), so a data-only environment is never left open.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/manageddatabase/netpol.go` in the control-plane repo
- `internal/operators/workload/netpol.go` in the control-plane repo

<a id="managed-postgres-role-secrets"></a>

### Credentials the platform never holds

The database owner credential is generated in-cluster and published to your workloads; the control plane holds no credential that can open a tenant database.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/manageddatabase/rolesecrets.go` in the control-plane repo
- `internal/operators/workload/database_identity.go` in the control-plane repo

---

<a id="ship"></a>

## Ship

Any number of environments, each declared as a directory of KCL. A release is built once, recorded as an immutable, content-addressed set of artifacts and promoted between environments by that record, so what ran in staging is what runs in prod. This is the part of Forge that most reduces deploy-day toil.

<a id="build-once-promote-by-digest"></a>

### Build once, promote by digest

`forge env build --release` records an immutable set of artifact digests, and `forge env deploy prod --from staging` deploys exactly the bytes that passed staging.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env build <env> --release <v>`, then `forge env deploy prod --from staging`.

**Why:** A pipeline that rebuilds per environment ships something slightly different from what it tested. `--from` works within one control plane; `--from-promotion` pins the source promotion and the deploy exits 3 `source_moved` if it moved.

**Evidence:**

- command `forge env build --release`
- command `forge env deploy --from`
- library [`pkg/release`](../pkg/release)
- skill `deploy/release` (`forge skill load deploy/release`)
- command `forge env build`
- command `forge env deploy`
- skill `deploy/build` (`forge skill load deploy/build`)

<a id="workload-once-bind-per-env"></a>

### Deploy anywhere, declare once

A `fw.Workload` is declared once in `workloads.k` and each env binds it to host processes, Docker Compose, a Kubernetes cluster you run, Reliant hosting, your own deploy tooling or build-only, so no env file restates a command or build that could drift.

_Enforced · Forge, on any infrastructure_

**Use:** `OnHost`, `OnCompose`, `OnCluster`, `OnHosted`, `OnExternal` or `BuildOnly` per env; `forge env render|shape|diff` show the result.

**Evidence:**

- source [`pkg/deploy/workload_render.go`](../pkg/deploy/workload_render.go)
- source [`internal/deploytarget/compose.go`](../internal/deploytarget/compose.go)
- source [`internal/deploytarget/hosted.go`](../internal/deploytarget/hosted.go)
- source [`internal/deploytarget/firebase.go`](../internal/deploytarget/firebase.go)
- skill `deploy/model` (`forge skill load deploy/model`)
- command `forge env render`

<a id="external-deploy-tooling"></a>

### Keep your deploy tool

A workload bound to `forge.OnExternal` is built, released and promoted by forge and then shipped by your own Helm, Fly.io, Cloud Run, ECS or Argo CD through four optional argv hooks, so adopting forge does not mean replacing the pipeline that deploys today.

_Enforced · Forge, on any infrastructure_

**Use:** `wl.api | {runtime = forge.OnExternal {status = [...], plan = [...], deploy = [...], health = [...]}}`; no hooks at all is record-only.

**Why:** Hooks are argv with no shell, and receive the frozen `repo@sha256:…` as `$FORGE_IMAGE`, so the bytes your tool ships are the bytes the release recorded. The deploy lands in the same ledger and deploy markers as every other runtime, and `forge env status` reads the running digest through the `status` hook. Declare a `forge.ControlPlane` and the env is self-managed: the control plane records every promotion (and your CI's `forge release record`), and its hosted secrets stay write-only.

**Evidence:**

- source [`kcl/workload.k`](../kcl/workload.k)
- source [`internal/deploytarget/external.go`](../internal/deploytarget/external.go)
- source [`internal/cli/hosted_env_resolver.go`](../internal/cli/hosted_env_resolver.go)
- command `forge env deploy`
- command `forge env status`
- skill `deploy/external` (`forge skill load deploy/external`)

<a id="env-verify-parity"></a>

### Prove parity before deploying

`forge env verify <env>` compares, per workload, the digest the env's promotion pins against the digest running and runs your tool's own dry run, while building, recording and deploying nothing, so forge's first deploy of an existing project is known to change nothing.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** `forge project new --in-place --kind app`, bind the workload to `forge.OnExternal {status = [...], plan = [...]}` in deploy/kcl/<env>/main.k, then `forge env verify <env> --image api=<ref>@sha256:…`. **Agent time:** ~1 hour.

**Use:** `forge env verify prod`, or `--image api=repo@sha256:…` before any forge release exists; `--json` for CI. Exit 0 match, 1 drift, 2 not verified.

**Why:** A missing `status` or `plan` hook is reported unverified, never as "no diff". Cluster workloads are checked off the cluster and against the release's recorded bundle with a client-side live diff.

**Evidence:**

- command `forge env verify`
- command `forge env verify --image`
- source [`internal/cli/env_verify_cmd.go`](../internal/cli/env_verify_cmd.go)
- skill `deploy/external` (`forge skill load deploy/external`)

<a id="prod-from-day-0"></a>

### Production from the first commit

What `forge project new` scaffolds on the first commit is what runs in production, configured differently, so there is no toy mode to replace before shipping.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project new`
- source [`internal/templates/deploy/kcl`](../internal/templates/deploy/kcl)

<a id="envs-in-kcl"></a>

### Environments as typed code

dev, staging and prod are typed KCL from day 0, and `forge ci validate-kcl` proves every env renders manifests the cluster will accept, so a broken deploy declaration fails CI, not the deploy.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/templates/deploy/kcl`](../internal/templates/deploy/kcl)
- command `forge ci validate-kcl`
- command `forge env render`
- command `forge env shape`

<a id="env-new"></a>

### New environment in one command

`forge env new` scaffolds an environment from an existing one, and `forge env options` lists the render options it takes.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge env new`
- command `forge env options`

<a id="env-diff"></a>

### Diff your code against deployed

`forge env diff` renders this checkout and shows how each environment would change.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge env diff`
- command `forge release bundle`

<a id="declared-env-catalog"></a>

### Every env in the release

A released bundle carries one `declared_envs` entry per `deploy/kcl/<env>/main.k`, so the console lists environments that never recorded anything, and `forge env status --json` with no env prints the whole project's topology.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- library [`pkg/topology`](../pkg/topology)
- command `forge env status`
- skill `deploy/shape` (`forge skill load deploy/shape`)
- source [`internal/bundle/declared_envs_test.go`](../internal/bundle/declared_envs_test.go)

<a id="release-ledger"></a>

### Release timeline per environment

Every promotion is an append-only ledger entry, so `forge env releases` answers what was live at any moment (or for any trace id) and what changed: commits, PRs, shape diff, migrations, gates, health.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env releases at prod 2026-10-21T14:05:00Z`, or `at` a trace id.

**Evidence:**

- command `forge env releases list`
- command `forge env releases show`
- command `forge env releases at`
- command `forge release where`
- skill `observability/query` (`forge skill load observability/query`)
- library [`pkg/release`](../pkg/release)

<a id="record-deploys"></a>

### Record deploys from existing CI

One `forge release record` step after the deploy job your pipeline already runs gives monitoring a deploy marker per release, in any repo and any language, with no deploy/kcl and nothing built or applied by forge.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** Add a forge.yaml with `name:`, then after your deploy job run `forge release record --env prod --version "$TAG" --workload api --image <ref>@sha256:…` with `FORGE_CONTROL_PLANE_TOKEN` set. **Agent time:** ~15 min.

**Use:** Works in any repo: add `forge.yaml` with `name:`, store a token with deploy:write, minted in the web app (Settings → API tokens), as `FORGE_CONTROL_PLANE_TOKEN`, then after the deploy step run `forge release record --env prod --version "$TAG" [--workload api --image ghcr.io/acme/api@sha256:…]` and `forge obs check prod --release "$TAG" --wait`. Easy, about 15 minutes: forge.yaml 1 min, CI secret 5 min, the step 5 min, a first run 4 min. Recipes for GitHub Actions, GitLab CI and Buildkite are in `forge skill load deploy/record`.

**Why:** The release is record-only: it may name no artifacts (a version and a commit), it is labelled "recorded by CI" in `forge obs changes` and the console, and the control plane refuses to bind one to an env forge itself deploys, so a record never poses as forge's own deploy. A retried CI step records once, and the same version with other digests is a conflict, not an overwrite. Not signed in, it records on this machine's ledger.

**Evidence:**

- command `forge release record`
- skill `deploy/record` (`forge skill load deploy/record`)
- source [`internal/cli/release_record.go`](../internal/cli/release_record.go)
- library [`pkg/release`](../pkg/release)

<a id="deploy-plan-approval"></a>

### Deploy plans with binding approval

`forge env deploy` prints its plan and writes nothing until confirmed, `--plan-only` then `--approve <digest>` refuses a plan that changed in between, and a data-loss finding needs an explicit acknowledgement even with `--yes`.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env deploy <env> [--plan-only|--approve <digest>|--acknowledge-destructive <code>]`

**Why:** With no terminal and no `--yes` the deploy exits 5 having built, pushed and cut but promoted nothing. A plan that changed between stages is refused with exit 3 `plan_stale` rather than re-approved blind, and stop-class findings (`stateful_deletion`, `lb_identity_change`) need `--acknowledge-destructive <code>`, so a `--yes` hard-coded in CI cannot pre-approve a future data loss. Plan field paths name list items by `name` and never carry a value, so a plan is safe to print into CI logs.

**Evidence:**

- command `forge env deploy`
- source [`internal/cli/deploy_plan_gate.go`](../internal/cli/deploy_plan_gate.go)
- source [`internal/cli/deploy_confirm.go`](../internal/cli/deploy_confirm.go)
- skill `deploy/plan` (`forge skill load deploy/plan`)

<a id="polyrepo-pinned-sources"></a>

### Polyrepo, pinned, any language

Teams outside a monorepo, in any language, share an infra repo's KCL by a git pin in kcl.mod and build other repos' images from a pinned `DockerBuild.context`, with no go.mod and no CI checkout step; a release records each image's source repo and commit.

_Enforced · Forge, on any infrastructure_

**Use:** `[forge_projects] infra = { repo = "github.com/acme/infra", ref = "v1.4.0", commit = "<sha>" }` in `deploy/kcl/kcl.mod`; `forge.DockerBuild {context = forge.GitSource {repo = …, ref = …}}`; `forge kcl imports [--refresh]`.

**Why:** A sibling path (`../billing`) builds whatever is checked out there and is absent in CI. A pin resolves to the same bytes on every machine through one shared cache, renders offline once fetched, and a `commit` lock refuses a moved tag as a pin mismatch. A gitignored `.forge/source-overrides.yaml` points a repo at a working copy while you develop both, and a release refuses to cut while one is in use.

**Evidence:**

- command `forge kcl imports --refresh`
- source [`internal/kclprojects/gitimport.go`](../internal/kclprojects/gitimport.go)
- source [`internal/cli/build_docker_source.go`](../internal/cli/build_docker_source.go)
- source [`docs/cross-repo-sources.md`](../docs/cross-repo-sources.md)
- skill `deploy/polyrepo` (`forge skill load deploy/polyrepo`)
- skill `deploy/kcl-imports` (`forge skill load deploy/kcl-imports`)

<a id="hosted-deploy-plan"></a>

### Server-side deploy plan

On Reliant hosting the plan is computed from the bundle last applied, not from the declared shape, with unknown fields refused, so the diff you approve is the change that happens.

_Enforced · Reliant hosting_

**Evidence:**

- command `forge env deploy --plan-only`
- `internal/deployplan/assemble.go` in the control-plane repo

<a id="promote-cas"></a>

### No stale deploy overwrites a hotfix

Every promote is a compare-and-set against the promotion the plan read, so a CI job that waited on approval cannot overwrite a hotfix that landed meanwhile, and a retry that already landed is a no-op.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env deploy <env> --expect-current <promotion-id>` (the default is the one the plan read).

**Why:** The expectation is a promotion id, not a version, because v1 to v2 to v1 makes "current is v1" ambiguous. A never-promoted env takes `--expect-current unbound` or `--expect-unbound`.

**Evidence:**

- source [`internal/cli/promote_cas.go`](../internal/cli/promote_cas.go)

<a id="rollout-not-trampled"></a>

### In-flight rollouts aren't trampled

Promoting while the current promotion is still rolling out is refused (exit 4) unless `--supersede` is given, and the override is recorded on the new entry.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge env deploy --supersede`
- source [`internal/cli/deploy.go`](../internal/cli/deploy.go)

<a id="exit-code-contract"></a>

### Exit codes pipelines can branch on

Every verb shares one exit-code table (0 success, 1 wrong, 2 could not tell, 3 conflict, 4 refused, 5 plan unconfirmed, 6 superseded, 7 queued on a person, 8 timed out), so a pipeline branches without parsing messages.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/exitcodes.go`](../internal/cli/exitcodes.go)

<a id="frozen-env"></a>

### Freeze an environment declaratively

An env whose reconcile policy is `pinned` refuses every promote with exit 4 `environment_pinned`, so a freeze is declared state, not a convention.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/exitcodes.go`](../internal/cli/exitcodes.go)

<a id="migrations-gate-rollout"></a>

### Migrations gate every rollout

Deployed envs apply migrations from a rendered initContainer under a Postgres advisory lock before any new pod starts, so replicas never race to apply one and no pod runs on an unmigrated schema.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`pkg/migratekit/runlock.go`](../pkg/migratekit/runlock.go)
- source [`pkg/deploy/workload_render.go`](../pkg/deploy/workload_render.go)
- command `forge ci migration-safety`
- skill `db/deploy-migrations` (`forge skill load db/deploy-migrations`)

<a id="migration-release-guard"></a>

### Out-of-order migrations refused

A release records the migrations it embeds, and the cut and deploy refuse one whose migrations the env's schema already passed, instead of discovering it as crash-looping pods.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env build <env> --release`; waive with `--allow-passed-migrations` or `--allow-unmerged-migrations`; backfill old releases with `forge env releases backfill-migrations`.

**Why:** This is the 2026-10-24 reliant incident, encoded.

**Evidence:**

- command `forge env releases backfill-migrations`
- command `forge env build`
- skill `db/release-guard` (`forge skill load db/release-guard`)

<a id="interrupted-cut-resume"></a>

### A half-pushed release resumes

A cut that died after pushing images is resumed from its journal by re-running the same build, rebuilding nothing.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env releases interrupted <env> <v>`

**Evidence:**

- command `forge env releases interrupted`
- skill `deploy/interrupted-cut` (`forge skill load deploy/interrupted-cut`)

<a id="ledger-portability"></a>

### Portable, verifiable release ledgers

`forge ledger where|show|export|import` says which backend holds an env's ledger and moves history between backends, `forge release verify` proves every artifact a release names exists and matches, and `forge release bundle` diffs a recorded bundle against the live clusters.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge ledger where`
- command `forge ledger export`
- command `forge release verify`
- command `forge release bundle`
- skill `deploy/offline-ledger` (`forge skill load deploy/offline-ledger`)
- skill `deploy/release` (`forge skill load deploy/release`)
- command `forge ledger import`

<a id="offline-breakglass"></a>

### Break-glass deploy without control plane

When the control plane is down, `forge env deploy <env> --offline-ledger --to previous` deploys a cached release by its recorded digests and replays the promotion on the next contact.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/cli/offline_ledger.go`](../internal/cli/offline_ledger.go)
- command `forge env deploy --offline-ledger`
- skill `deploy/offline-ledger` (`forge skill load deploy/offline-ledger`)

<a id="roll-forward"></a>

### Roll forward, deliberately

A failed deploy leaves the previous ReplicaSet serving and a bad migration is stopped before any workload changes, so recovery is a new release, and binding an older release is an ordinary promote labelled BEHIND in the plan and the ledger.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env deploy <env> --release <older> --note "<why>"`

**Why:** There is no rollback command and no down migration, deliberately; the plan and the ledger label an older release `direction BEHIND`. A per-env rollback floor ("never deploy anything older than X") is designed and not shipped; do not cite it until it is.

**Evidence:**

- command `forge env deploy --note`
- lint lane `forge lint --migration-safety`

<a id="release-health-gate"></a>

### Prod gated on staging's verdict

A declared `HealthGate` makes the control plane refuse a prod promote until staging holds a PASS for that release, inside the promotion's transaction, with an audited override for incidents.

_Enforced · Reliant hosting_

**Use:** `delivery_policy = forge.DeliveryPolicy { health_gate = forge.HealthGate { after = ["staging"] } }`

**Why:** Enforced by a control plane that advertises `shape-health-gate`; recorded only on a self-hosted env. The refusals are distinct: `health_gate_pending` (exit 4, the window is still open), `health_gate_unproven` (exit 4) and `health_gate_regressed` (exit 1). A release prod already ran with a PASS is admitted without staging, so a rollback needs no detour, and the incident override `--override-health-gate "<why>"` is recorded with who, why and what staging showed.

**Evidence:**

- library [`pkg/releasehealth`](../pkg/releasehealth)
- skill `deploy/delivery-policy` (`forge skill load deploy/delivery-policy`)

<a id="preview-environments"></a>

### Per-PR preview environments

Every pull request gets its own environment, a copy of a committed template env named for the PR, which deletes itself when it expires.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env preview create --pr 123 --ttl 72h`, then `forge env deploy preview-pr-123 --yes`; `forge env preview delete --pr 123 --yes` on close, `forge env gc --yes` on a schedule

**Why:** The instance is an ordinary env, so every env verb works on it, and its name depends only on the PR or branch, so the job that creates it and the job that deletes it agree with no shared state. Its expiry is recorded in the release shape (a control plane that advertises `shape-preview` deletes an expired hosted instance) and stamped on its namespaces, so `forge env gc` finds an expired preview on a cluster you operate from the cluster alone. `create` refuses an instance that would deploy into the template env's own namespace.

**Evidence:**

- command `forge env preview create`
- command `forge env gc`
- skill `deploy/preview` (`forge skill load deploy/preview`)
- library [`pkg/release`](../pkg/release)

<a id="promotion-required-gates-approval-hook"></a>

### Required checks and your approvals

An env refuses a promotion that does not carry its required checks as passed, or that your own approval system did not approve, enforced where the env's ledger lives so no client can skip it.

_Enforced · Forge, on any infrastructure_

**Use:** `delivery_policy = forge.DeliveryPolicy { required_gates = forge.RequiredGates { names = ["e2e"] }, approval_hook = forge.ApprovalHook { url = "https://approvals.example.com/forge" } }`, then `forge env deploy prod <v> --gate name=e2e,status=passed`

**Why:** A control plane that advertises `enforces-promotion-checks` judges both at promote; for an env without one, forge judges them before its own write and runs an `argv` hook with no shell. The refusals are distinct: `required_gate_missing` (exit 4), `required_gate_failed` (exit 1), `approval_denied` (exit 4; a hook that times out or fails denies) and `approval_pending` (exit 4, retry once approved). `--yes` never answers for the hook, and each verdict is recorded on the promotion. `audit` records what `enforce` would refuse.

**Evidence:**

- skill `deploy/delivery-policy` (`forge skill load deploy/delivery-policy`)
- source [`internal/cli/promote_policy_checks.go`](../internal/cli/promote_policy_checks.go)
- source [`pkg/release/promotion_checks.go`](../pkg/release/promotion_checks.go)

<a id="shared-env-base"></a>

### One env base, per-env deltas

Every deployed env binds its workloads through one shared base, so an env file holds only what is its own and a new env starts as its deltas.

_Enforced · Forge, on any infrastructure_

**Use:** `deploy/kcl/base/env.k` (scaffolded by `forge project new`), imported by each env as `import ..base.env as envbase`; `forge env new <name> --from <env>` copies only the source env's own values

**Why:** It is plain KCL, so `forge env render`, `env diff` and `env shape` read it like any import, and an env overrides a shared binder by wrapping it. The scaffold tools keep editing each env's one binding line per workload.

**Evidence:**

- source [`internal/templates/deploy/kcl/base/env.k.tmpl`](../internal/templates/deploy/kcl/base/env.k.tmpl)
- command `forge env new`
- skill `deploy/model` (`forge skill load deploy/model`)

<a id="release-health-policy"></a>

### Release health policy as code

Thresholds and the failing set are `forge.ReleaseHealth` on each env, recorded in the release, so the CLI, the console and alerting judge a release the same way.

_Enforced · Forge, on any infrastructure_

**Use:** `release_health = forge.ReleaseHealth { ... }` in `deploy/kcl/<env>/main.k`; `--threshold`, `--fail-on` and `--window` override one call.

**Why:** The thresholds are `window`, `min_requests`, `max_error_rate_increase`, `max_p95_ratio` and `new_issue_min_events`, the failing set is `fail_on`, and the check's document says where each effective value came from. A service is compared only with `min_requests` in both windows, and `require_baseline` (`any`, `every`, `none`) says how many services need that baseline before a release can pass. `--wait --early-pass 8m` ends a wait with a PASS only once 8 minutes are observed, every judged service reached its own `min_requests` on the new version, and nothing fired.

**Evidence:**

- source [`pkg/releasehealth/releasehealth.go`](../pkg/releasehealth/releasehealth.go)
- command `forge obs check`

<a id="delivery-policy-recorded"></a>

### Delivery policy, stated honestly

`forge.DeliveryPolicy` declares per env what a release must prove, each control `off`, `audit` or `enforce`: `health_gate`, `signing` and `flux_verify` are enforced on a control plane, `required_gates` and `approval_hook` wherever the env's ledger lives, while `provenance_sbom`, `trusted_builder`, `admission_verify`, `signed_promotions`, `vuln_gate`, `bundle_kinds` and `approvals` are recorded and refuse nothing yet.

_Recorded · Forge, on any infrastructure_

**Use:** `delivery_policy = forge.DeliveryPolicy { ... }` in `deploy/kcl/<env>/main.k`

**Why:** Setting a recorded control to `enforce` today is a declaration of intent. `forge env build` does not sign images; only an env with a control plane gets its bundles signed, by the platform. A control that cannot block a deploy defaults to `enforce`; one that can defaults to `audit`, so you read its would-deny verdicts before turning it on.

**Evidence:**

- skill `deploy/delivery-policy` (`forge skill load deploy/delivery-policy`)
- library [`pkg/approval`](../pkg/approval)

<a id="gate-evidence"></a>

### Check results attached to releases

`forge gate record` attaches a lint, test, smoke or manual sign-off result to the promotion it is about, with who and when, and `forge env deploy --gate` reads them; nothing refuses a promotion on a gate's status.

_Recorded · Forge, on any infrastructure_

**Evidence:**

- command `forge gate record`
- command `forge gate list`

<a id="deploy-waits-for-health"></a>

### Deploys wait, then smoke-test

`forge env deploy` records, applies and waits for health, and `forge env smoke` probes every declared ingress route for TLS, routing and CORS.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge env deploy`
- command `forge env smoke`

<a id="config-check-before-push"></a>

### Config errors caught before push

`forge env build --push`, `--release` and `forge env deploy` boot each workload with its rendered env under `FORGE_CONFIG_CHECK=1` before pushing, so a missing required config value is found before the image is public.

_Enforced · Forge, on any infrastructure_

**Use:** `--config-check=gate|warn|off`

**Evidence:**

- command `forge env build`
- skill `deploy/config-check` (`forge skill load deploy/config-check`)

<a id="flux-safe"></a>

### Reconciler-safe manifests

Forge refuses a Job that a GitOps reconciler would re-run or wedge (a TTL, an unhashed name) and a force-recreate on a kind that holds data at bundle build, and a bare `forge lint` catches them earlier across every env.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint`
- skill `deploy/flux-safe` (`forge skill load deploy/flux-safe`)
- skill `deploy/flux` (`forge skill load deploy/flux`)

<a id="overrides-byo-yaml"></a>

### Override any field, bring any YAML

`Bundle.overrides`, keyed by `Kind/name`, changes one field of a rendered object without forking the template (a conflict fails the render), and `forge.Manifests` brings your own YAML or Helm chart.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- skill `deploy` (`forge skill load deploy`)
- skill `deploy/overrides` (`forge skill load deploy/overrides`)
- skill `deploy/byo-yaml` (`forge skill load deploy/byo-yaml`)

<a id="cluster-connect"></a>

### Connect a cluster you run

`forge cluster connect` registers a Kubernetes cluster you run as one env's binding, with a ServiceAccount token scoped to exactly what the hub applies.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge cluster connect`
- skill `deploy/cluster-connect` (`forge skill load deploy/cluster-connect`)

<a id="static-site-first-class"></a>

### Static sites are full projects

A landing page, docs site, SPA or PWA gets dev, staging and prod, build-once releases promoted by digest and per-env runtime config without a rebuild, and an existing Vite or Next.js app is adopted in place.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In a Vite or Next.js app, `forge project new --in-place --kind site --name <name>` writes forge.yaml and deploy/kcl and leaves your code alone. **Agent time:** ~10 min.

**Use:** `forge project new site --kind site`, or `forge project new --in-place --kind site`.

**Evidence:**

- command `forge project new`
- source [`internal/codegen/frontend_config_ts.go`](../internal/codegen/frontend_config_ts.go)
- source [`internal/linter/staticexport/nextconfig.go`](../internal/linter/staticexport/nextconfig.go)
- skill `deploy/static-site` (`forge skill load deploy/static-site`)

<a id="generated-ci-workflows"></a>

### Generated CI and deploy workflows

A new project gets `ci.yml`, `proto-breaking.yml`, `build-images.yml`, a per-env `deploy.yml`, `e2e.yml` and `pre-commit.yml`, each labelled regenerated or yours, and each a thin wrapper around the local tasks.

_Enforced · Forge, on any infrastructure_

**Use:** `ci.gate.extra_checks` adds a check; from CI, `forge env build <env> --push` then `forge env deploy <env> --yes`.

**Evidence:**

- source [`internal/generator/ci_workflows.go`](../internal/generator/ci_workflows.go)
- source [`internal/templates/ci/github/ci.yml.tmpl`](../internal/templates/ci/github/ci.yml.tmpl)
- source [`internal/templates/ci/github/deploy.yml.tmpl`](../internal/templates/ci/github/deploy.yml.tmpl)
- source [`internal/templates/ci/github/proto-breaking.yml.tmpl`](../internal/templates/ci/github/proto-breaking.yml.tmpl)
- source [`internal/templates/ci/github/forge-deploy-action.yml.tmpl`](../internal/templates/ci/github/forge-deploy-action.yml.tmpl)
- skill `ci` (`forge skill load ci`)
- skill `ci/extending` (`forge skill load ci/extending`)

<a id="ci-evidence-tools"></a>

### CI runs you can read back

`forge ci summarize` renders any `--json` verdict as a job summary, and `forge ci run` shows one run's timeline across the cut, checks, promotes and rollouts.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge ci verify-test-run`
- command `forge ci test-budget`
- command `forge ci summarize`
- command `forge ci run`

---

<a id="host"></a>

## Host

Optional Reliant hosting runs what forge builds: services, workers and jobs, managed Postgres, static sites behind a CDN and custom domains, with no cluster of your own. Rebinding an environment to a cluster you run is a one-line change.

<a id="managed-postgres"></a>

### Managed Postgres

An environment on Reliant hosting gets Postgres the platform provisions and operates, each database its own server in your (org, environment) namespace, so no other tenant shares it and staging can never get a credential for prod's data.

_Enforced · Reliant hosting_

**Use:** `forge.ManagedDatabase { spec = { instances = 1..3, storageGiB = 1..500 } }`, then `forge env deploy <env>`.

**Evidence:**

- `internal/operators/manageddatabase` in the control-plane repo
- skill `deploy/hosting` (`forge skill load deploy/hosting`)
- source [`pkg/deploy/v1alpha1/manageddatabase_types.go`](../pkg/deploy/v1alpha1/manageddatabase_types.go)
- source [`kcl/tiers/tiers_gen.k`](../kcl/tiers/tiers_gen.k)
- `internal/operators/manageddatabase/doc.go` in the control-plane repo
- `internal/operators/manageddatabase/controller.go` in the control-plane repo

<a id="static-hosting-cdn"></a>

### Static hosting with a CDN

A static site is served from a platform-owned bucket behind a CDN, invalidated on release, with per-env runtime config without a rebuild and old releases retained.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/staticsite/cloudcdn` in the control-plane repo
- `internal/operators/staticsite/invalidation.go` in the control-plane repo
- `internal/operators/staticsite/runtimeconfig.go` in the control-plane repo
- `internal/operators/staticsite/retention.go` in the control-plane repo

<a id="custom-domains-tls"></a>

### Custom domains with automatic HTTPS

Claim a hostname, publish the DNS records forge prints, and the platform verifies it, issues and renews the certificate and serves it, with bind and rebind between envs and no spec edit.

_Enforced · Reliant hosting_

**Use:** `forge domain add|bind|verify|ls|show|unbind|rm`

**Evidence:**

- command `forge domain add`
- command `forge domain bind`
- `internal/customdomain` in the control-plane repo
- `internal/domaincerts` in the control-plane repo
- `docs/custom-domains.md` in the control-plane repo
- command `forge domain verify`
- source [`internal/deploytarget/hosted_domains.go`](../internal/deploytarget/hosted_domains.go)
- skill `deploy/domains` (`forge skill load deploy/domains`)

<a id="hosted-by-default"></a>

### Staging and prod hosted by default

A new project's staging and prod are bound to Reliant hosting (managed Postgres, the API as a workload, the frontend on a CDN, per-env secrets and custom domains) and rebinding to your own cluster is a one-line change.

_Enforced · Reliant hosting_

**Use:** `forge env deploy staging`, then `forge env deploy prod --from staging`.

**Evidence:**

- command `forge env deploy`
- `internal/operators/workload` in the control-plane repo
- `internal/operators/staticsite` in the control-plane repo
- `internal/operators/manageddatabase` in the control-plane repo
- skill `deploy/hosting` (`forge skill load deploy/hosting`)

<a id="claimable-deploys"></a>

### Deploy with no account

`forge env deploy <env> --claimable` publishes one static site, or one service with one database, with no signup and prints a live URL plus a claim link valid 72 hours.

_Enforced · Reliant hosting_

**Use:** `forge env deploy <env> --claimable`

**Why:** It is refused when you are signed in; opening the claim link signs the person up free and makes the deploy theirs.

**Evidence:**

- command `forge env deploy --claimable`
- source [`internal/cloud/claimable.go`](../internal/cloud/claimable.go)
- `internal/claimable` in the control-plane repo
- `internal/operators/workload/claimable.go` in the control-plane repo
- source [`internal/cli/deploy_claimable.go`](../internal/cli/deploy_claimable.go)
- source [`internal/cli/claimable_limits.go`](../internal/cli/claimable_limits.go)

<a id="free-static-site"></a>

### One static site free

One hosted static site is free per owning person, counted across every env and every org they own without a plan, with metered egress above the free tier.

_Enforced · Reliant hosting_

**Why:** The allowance is per person, not per org, because a per-org allowance would multiply free sites (owner decision, 2026-10-26).

**Evidence:**

- `internal/freesite` in the control-plane repo
- `internal/staticfreetier` in the control-plane repo

<a id="live-hostname"></a>

### A live URL on deploy

Every hosted app and site gets its own platform hostname the moment it deploys.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/staticsite/hostname.go` in the control-plane repo
- `internal/hostname` in the control-plane repo

<a id="hosted-workloads"></a>

### Services, workers and jobs hosted

Services, workers and gating jobs run hosted under a restricted profile (read-only root filesystem, no service-account token, restricted pod security, quota and egress containment); cron-kind workloads and operators need a cluster you run.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/workload/doc.go` in the control-plane repo
- `internal/operators/workload/decorate.go` in the control-plane repo

<a id="hosted-registry"></a>

### A private registry included

Hosted envs push to a per-org registry subtree authenticated by your Reliant token, so there is no registry account to create and the platform admits only your own images.

_Enforced · Reliant hosting_

**Evidence:**

- command `forge registry login`
- command `forge lint`
- `internal/ociregistry` in the control-plane repo
- `internal/registryauth` in the control-plane repo

<a id="hosted-not-ready-policy"></a>

### Broken workloads stop billing

A workload with no ready replica past its limit is scaled to zero and marked failed instead of crash-looping forever.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/workload/notready.go` in the control-plane repo

<a id="billing-queued-exit7"></a>

### Billing never fails silently

A deploy that needs billing is recorded and queued (exit 7) with the link to fix it, and proceeds on its own when the hold clears.

_Enforced · Reliant hosting_

**Use:** `forge env deploy <env> --wait` blocks until it is live.

**Why:** A capacity pre-flight runs before anything builds.

**Evidence:**

- source [`internal/cli/deploy_queued.go`](../internal/cli/deploy_queued.go)
- source [`internal/cli/env_wait.go`](../internal/cli/env_wait.go)
- `internal/deployhold` in the control-plane repo
- skill `deploy/hosted-capacity` (`forge skill load deploy/hosted-capacity`)

<a id="hosted-env-lifecycle"></a>

### Env lifecycle from the CLI

`forge env stop`, `start` and `delete` operate a hosted env (delete shows what goes and refuses while a custom domain is bound), and `forge env list --orphaned` finds hosted envs the project no longer declares.

_Enforced · Reliant hosting_

**Evidence:**

- command `forge env deploy`
- `internal/operators/workload/lapse.go` in the control-plane repo
- command `forge env stop`
- command `forge env delete`
- command `forge env list`
- skill `deploy/lifecycle` (`forge skill load deploy/lifecycle`)

<a id="llm-no-key"></a>

### LLM access without a key

Declare `forge.LLM { runtime = forge.OnHosted {} }` and give a workload `forge.llm_env()`, and the platform puts a per-env, capped, billed key in the pod for the official OpenAI and Anthropic SDKs, unmodified, so the app never holds a provider key.

_Enforced · Reliant hosting_

**Why:** Reliant's gateway speaks both wire formats, streaming and image generation included, so `anthropic.NewClient()` reads `ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY` unmodified; dev and a cluster you run use `forge.LLMEndpoint`, including a local Ollama.

**Evidence:**

- skill `llm` (`forge skill load llm`)
- `internal/operators/workload/llminject.go` in the control-plane repo
- `internal/llmgateway` in the control-plane repo
- skill `deploy/llm` (`forge skill load deploy/llm`)

<a id="managed-postgres-placement"></a>

### Databases on stable nodes

Database instances are placed on non-scale-to-zero nodes so the autoscaler never reclaims a node holding your data.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/manageddatabase/placement.go` in the control-plane repo

<a id="web-forge-console"></a>

### A console for every project

The Forge console (forge.reliantlabs.io, also inside the Reliant web app) covers every project: workloads, releases, observability and dashboards, data, storage, environments, secrets, domains, monitoring and activity.

_Enforced · Reliant harness_

**Evidence:**

- `frontends/web/src/forgeConsoleRoutes.tsx` in the reliant repo
- `frontends/web/src/forgeShellRoutes.tsx` in the reliant repo

<a id="hosted-console-data-storage-browser"></a>

### Data and storage browsers

The web console browses an environment's database and its buckets read-only, scoped to owned, ready resources of your org.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/storagebrowse` in the control-plane repo
- `internal/handlers/storage_browser` in the control-plane repo
- `frontends/web/src/forgeConsoleRoutes.tsx` in the reliant repo

---

<a id="run"></a>

## Run

Logs, traces and metrics on every boundary from day 0, the same signals locally and in production, and a post-deploy check that proves a release good from its telemetry. The signals are OpenTelemetry and the app never learns the backend: it reads the standard `OTEL_*` variables.

<a id="local-clickstack"></a>

### Observability stack included

`forge env up` brings up ClickHouse, HyperDX and an OTLP collector in one container on loopback ports, with logs, traces and metrics sources pre-created and no sign-in, so you can debug from the first boot.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env up dev`; opt out with `_observability = False`.

**Evidence:**

- command `forge env up`
- source [`internal/clickstack/clickstack.go`](../internal/clickstack/clickstack.go)

<a id="default-alerts"></a>

### Production alerts out of the box

Every env gets default alert rules for error rate, latency, silent services, frontend errors and LCP, new, regressed and spiking issues, regressed releases, anomalies, uptime, certificates, crash loops, OOM kills, memory, image pulls, evictions, failed deploys and observability quota, evaluated locally and delivered in-app, to Slack or to PagerDuty when hosted.

_Enforced · Forge, on any infrastructure_

**Why:** The defaults are `ServiceErrorRateHigh`, `ServiceLatencyP95High`, `ServiceSilent`, `FrontendJSErrorSpike`, `FrontendLCPPoor`, `NewIssue`, `IssueRegressed`, `IssueSpike`, `ReleaseRegressed`, `ServiceAnomaly`, `UptimeCheckFailing`, `UptimeCheckDown`, `CertificateExpiring`, `CertificateExpiringSoon`, `WorkloadCrashLooping`, `WorkloadOOMKilled`, `WorkloadMemoryNearLimit`, `WorkloadImagePullFailed`, `WorkloadEvicted`, `DeployFailed`, `ObservabilityQuotaOver100` and `ObservabilityQuotaOver150`, each tunable or off. Error-rate rules read tail-sampled spans correctly rather than over-stating the rate.

**Evidence:**

- source [`internal/obsdash/alerts.go`](../internal/obsdash/alerts.go)
- library [`pkg/alerting`](../pkg/alerting)
- command `forge obs alerts`
- `docs/observability/builtin-alerts.md` in the control-plane repo
- `internal/alerting` in the control-plane repo
- `internal/handlers/alerting/helpers.go` in the control-plane repo

<a id="obs-check"></a>

### Prove each deploy is good

`forge obs check <env>` compares the window after a promotion with the equal window before it, per service, lists what the release opened, and exits pass, fail, could not tell or timed out.

_Enforced · Forge, on any infrastructure_

**Use:** `forge obs check prod --wait --json`

**Why:** Error rate is weighted for tail sampling, client-cancelled requests are not errors, and latency is judged per endpoint. A gate must treat exit 2 as "not proven good", never as a pass.

**Evidence:**

- command `forge obs check`
- library [`pkg/releasehealth`](../pkg/releasehealth)
- skill `observability/query` (`forge skill load observability/query`)

<a id="edge-telemetry"></a>

### Every request traced and measured

Each RPC is one span, one metric sample and one log record at the edge with a W3C trace and request id, and every component call and ORM query is a child span — no hand-written tracing.

_Enforced · Forge, on any infrastructure_

**Use:** Nothing to do; opt a package out with `// forge:no-observe`, read it with `forge obs trace`.

**Why:** The RPC interceptor chain (`serverkit.NewChain`) records the span, the metric sample and the log record for every call. A package whose constructor returns its contract gets a generated `middleware_gen.go` decorator through an owned `observe_chain.go` seam, and the ORM registers a `bunotel` hook, so internal calls and queries show up in traces with no hand-written span.

**Evidence:**

- library [`pkg/observe`](../pkg/observe)
- library [`pkg/serverkit`](../pkg/serverkit)
- marker `// forge:no-observe`
- skill `observability/component-chain` (`forge skill load observability/component-chain`)
- library [`pkg/orm`](../pkg/orm)

<a id="one-log-path"></a>

### One log path, never double-counted

The server writes JSON lines to stdout with its process resource; locally a collector tails `.forge/logs/<env>/*.log`, on a cluster a node agent reads container logs, and `trace_id` and `span_id` become each line's trace link.

_Enforced · Forge, on any infrastructure_

**Why:** The stdout path and the OTLP logs exporter are mutually exclusive by design, so a line is never stored twice.

**Evidence:**

- skill `observability` (`forge skill load observability`)
- library [`pkg/observe`](../pkg/observe)

<a id="probes-graceful-shutdown"></a>

### Probes and zero-drop shutdown

A service gets `/readyz` and `/healthz`, and the pod's grace period is derived from the drain settings, so a rollout never kills a pod mid-drain and a draining replica reports its in-flight calls as canceled, not as errors.

_Enforced · Forge, on any infrastructure_

**Why:** The grace period is `PRE_STOP_DELAY + SHUTDOWN_TIMEOUT` + 5s.

**Evidence:**

- source [`pkg/serverkit/serverkit.go`](../pkg/serverkit/serverkit.go)
- source [`pkg/serverkit/pooler_drain.go`](../pkg/serverkit/pooler_drain.go)
- source [`pkg/lifecyclekit/lifecyclekit.go`](../pkg/lifecyclekit/lifecyclekit.go)
- command `forge doctor`
- library [`pkg/serverkit`](../pkg/serverkit)
- library [`pkg/lifecyclekit`](../pkg/lifecyclekit)
- library [`pkg/middleware`](../pkg/middleware)
- skill `deploy/model` (`forge skill load deploy/model`)

<a id="telemetry-proven"></a>

### Proves telemetry actually arrives

`forge env status` reads the store itself and reports which services sent traces, metrics, logs and profiles in the last 15 minutes, and missing evidence is UNDETERMINED, never PASS.

_Enforced · Forge, on any infrastructure_

**Use:** `forge env status dev --signal traces|metrics|logs|profiles|app`

**Evidence:**

- command `forge env status`

<a id="obs-cli"></a>

### Query prod from the shell

`forge obs logs`, `traces`, `trace`, `metrics`, `query` and the rest answer against the local ClickStack or a hosted env through one query compiler, as JSON with runnable `suggested[]` next commands.

_Enforced · Forge, on any infrastructure_

**Use:** `forge obs logs prod --service api --severity error --json`; `--explain` prints the compiled statements.

**Why:** Each verb answers in one `forge.obs.<verb>/v1` document carrying `ok`, `exit_code`, `scope`, bounded lists with `next_page_token` and up to three runnable `suggested[]` follow-ups, so a query that works locally means the same against prod. Raw SQL goes through a read-only guard.

**Evidence:**

- library [`pkg/obsquery`](../pkg/obsquery)
- command `forge obs logs`
- command `forge obs trace`
- command `forge obs query`
- command `forge obs metrics query`
- skill `observability/query` (`forge skill load observability/query`)

<a id="obs-status-changes"></a>

### What's wrong, what changed

`forge obs status` summarizes services, open issues, firing alerts and the release in force; `forge obs changes` lists promotions, rollbacks, failed deploys and activity.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge obs status`
- command `forge obs changes`

<a id="readable-traces"></a>

### Traces that show missing spans

`forge obs trace` prints a capped tree with errors and the critical path expanded, linked logs, and how much time went to uninstrumented code.

_Enforced · Forge, on any infrastructure_

**Why:** Each span says how it ended (`rpc=CANCELED`, `http=403`), and any attribute filters, e.g. `--filter span.attr.chat.id=<id>`.

**Evidence:**

- command `forge obs trace`

<a id="error-fingerprints"></a>

### Stacks and stable error fingerprints

Server faults carry a bounded stack and a fingerprint folding in the normalized cause, so one outage is one issue, not six.

_Enforced · Forge, on any infrastructure_

**Why:** Internal, Unknown, DataLoss and Unavailable errors capture a capped stack into `exception.stacktrace`, and the fingerprint folds in the normalized cause (the SQLSTATE, the refused dial target), so one "Transaction failed" is not six different outages in one issue.

**Evidence:**

- command `forge obs issues`
- library [`pkg/svcerr`](../pkg/svcerr)
- skill `observability/error-stacks` (`forge skill load observability/error-stacks`)

<a id="issues-triage"></a>

### Error issues tied to releases

Errors group into issues with culprit, frames, trace, events per version and suspect commits, and `resolve --in next` ties a fix to a release so a later event from an older one is a regression.

_Enforced · Forge, on any infrastructure_

**Use:** `forge obs issue <id>`; `forge obs issues --since-release|--since-deploy` lists what a release introduced.

**Why:** Issues are fingerprinted with one algorithm, at query time locally and at ingest on the control plane; issues that share a cause and first fired together collapse into one incident without being merged, and triage (`resolve`, `ignore`, `assign`, `comment`) is a verb.

**Evidence:**

- command `forge obs issue`
- command `forge obs issues`
- command `forge obs issue resolve`
- command `forge obs issue ignore`
- command `forge obs issue assign`
- skill `observability/query` (`forge skill load observability/query`)

<a id="issues-symbolication"></a>

### Grouped errors with real stacks

Browser and server errors are grouped into issues with triage verbs, and minified browser stacks are symbolicated from source maps that are never served publicly.

_Enforced · Reliant hosting_

**Evidence:**

- command `forge obs issues`
- command `forge obs issue resolve`
- `internal/issues` in the control-plane repo
- `internal/symbolicate` in the control-plane repo

<a id="declared-metrics"></a>

### Cardinality-safe declared metrics

Each metric is declared with its unit and attribute keys, and an undeclared attribute panics in development and is dropped and counted in production, so cardinality never silently explodes.

_Enforced · Forge, on any infrastructure_

**Use:** Declare instruments in `internal/metrics/metrics.go`; `<bin> metrics schema --json` and `forge obs metrics declared` print the set.

**Evidence:**

- command `forge obs metrics declared`
- command `forge obs metrics describe`

<a id="continuous-profiling"></a>

### Continuous profiling, no setup

Every process samples a CPU profile every five minutes and a heap snapshot every two hours over OTLP, and `forge obs profile` exports pprof, speedscope or collapsed stacks.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge obs profile`
- source [`pkg/observe/PROFILES.md`](../pkg/observe/PROFILES.md)
- library [`pkg/profilequery`](../pkg/profilequery)
- skill `observability/profiles` (`forge skill load observability/profiles`)

<a id="alerts-as-code"></a>

### Alerts as code, run locally

`forge.AlertRule`, `forge.AlertRoute` and `forge.AlertDefault` are KCL validated at render and recorded in the release, and `forge obs alerts check|run` evaluates them locally through a real Alertmanager.

_Enforced · Forge, on any infrastructure_

**Why:** An `AlertRule`'s `expr` is ClickHouse SQL, and every row it returns is a firing instance.

**Evidence:**

- command `forge obs alerts check`
- command `forge obs alerts run`
- source [`pkg/alerting/alertmanager.go`](../pkg/alerting/alertmanager.go)
- skill `observability/alerts` (`forge skill load observability/alerts`)

<a id="release-regressed-alert"></a>

### Bad releases flagged automatically

For an env on a control plane, every promotion is judged when its window closes, with the env's declared policy and no client overrides, and a FAIL raises `ReleaseRegressed` naming the findings, the release and its commit range.

_Enforced · Reliant hosting_

**Use:** `forge obs check <env> --release <v>` reproduces the verdict.

**Evidence:**

- command `forge obs check`
- `internal/releasehealth` in the control-plane repo
- `docs/observability/builtin-alerts.md` in the control-plane repo
- skill `observability/alerts` (`forge skill load observability/alerts`)

<a id="uptime-cert-probes"></a>

### Uptime and certificate probes

The platform probes a hosted env's public frontends and routes from outside on a schedule with no declaration, and alerts on failing checks and on certificates expiring within 14 and 3 days.

_Enforced · Reliant hosting_

**Use:** `forge.UptimeCheck` adds checks beyond the derived ones.

**Evidence:**

- `internal/uptimeprobe` in the control-plane repo
- `docs/observability/uptime-prober.md` in the control-plane repo
- source [`internal/obsdash/alerts.go`](../internal/obsdash/alerts.go)
- skill `observability/uptime` (`forge skill load observability/uptime`)

<a id="generated-dashboards"></a>

### Dashboards derived from code

Process and role dashboards are generated from the deployed commands and the composition graph, and `forge obs dashboards audit` replays an empty panel to say why it is empty.

_Enforced · Forge, on any infrastructure_

**Why:** Dashboards are HyperDX JSON in `deploy/observability/dashboards/`, and only `*_gen.json` is forge's.

**Evidence:**

- command `forge obs dashboards check`
- command `forge obs dashboards audit`
- command `forge obs dashboards add-panel`
- source [`internal/obsdash/bundleset.go`](../internal/obsdash/bundleset.go)
- library [`pkg/dashspec`](../pkg/dashspec)
- skill `observability/dashboards` (`forge skill load observability/dashboards`)
- skill `debug/dashboards` (`forge skill load debug/dashboards`)

<a id="builtin-product-analytics"></a>

### Built-in product analytics dashboards

A hosted app gets Users, Product and Acquisition dashboards and the Overview's Traffic and Users sections with no code: visitors, channels, sign-ups and their attribution, active and new users, retention, goals and funnels.

_In progress · Reliant hosting_

**Use:** `forge.Analytics` adds goals, an activation event and channel rules; a `<project>-users.json` replaces a dashboard; `dashboards = False` drops them.

**Why:** Visitors are counted by the platform's cookieless daily hash at the ingest edge, which ships with the control plane's edge columns and hash.

**Evidence:**

- library [`pkg/obsbuiltin`](../pkg/obsbuiltin)
- source [`pkg/obsbuiltin/population_clickhouse_test.go`](../pkg/obsbuiltin/population_clickhouse_test.go)
- skill `observability/product-analytics` (`forge skill load observability/product-analytics`)

<a id="release-markers"></a>

### Release markers on dashboards

Releases are marked on generated dashboards so a regression lines up with the deploy that caused it.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- source [`internal/obsdash/releases.go`](../internal/obsdash/releases.go)

<a id="rum-sessions"></a>

### Real-user sessions and replay

The browser SDK reports through a same-origin relay that never forwards cookies or `authorization`, and `forge obs session` prints what one user did as a text timeline; replay is opt-in and always masked.

_Enforced · Forge, on any infrastructure_

**Why:** The relay is same-origin (`/_otel`), and source maps are extracted to a registry sidecar and never served.

**Evidence:**

- command `forge obs sessions`
- command `forge obs session`
- source [`pkg/obsquery/rum_sessions.go`](../pkg/obsquery/rum_sessions.go)
- source [`pkg/obsquery/rum_replay.go`](../pkg/obsquery/rum_replay.go)
- skill `frontend-runtime/telemetry` (`forge skill load frontend-runtime/telemetry`)

<a id="telemetry-anywhere"></a>

### Send telemetry anywhere

`forge.Observability` names any OTLP endpoint, a headers secret reference, the signals and the sampling, and forge renders the collector for the runtime.

_Enforced · Forge, on any infrastructure_

**Why:** A local env can store its telemetry in the org's hosted store (`dev_telemetry`, `--telemetry=auto|hosted|local`).

**Evidence:**

- skill `observability/destination` (`forge skill load observability/destination`)
- source [`kcl/schema.k`](../kcl/schema.k)
- library [`pkg/observe`](../pkg/observe)

<a id="node-telemetry"></a>

### Telemetry for any Node process

`initNodeTelemetry` from `@reliantlabs/forge-web-runtime/node` ships a Node or Electron-main process's logs, uncaught errors and spans to any OTLP/HTTP endpoint, batched, with credential shapes redacted and no OpenTelemetry SDK to configure.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** `npm i @reliantlabs/forge-web-runtime @opentelemetry/api`, then `initNodeTelemetry({ serviceName, serviceVersion, endpoint })` at process start. **Agent time:** ~15 min.

**Evidence:**

- source [`web-runtime/src/node.ts`](../web-runtime/src/node.ts)
- source [`web-runtime/README.md`](../web-runtime/README.md)

<a id="real-opt-out"></a>

### Opt-out enforced at the source

Turning telemetry off for an env, or for one category (traces, metrics, logs, profiles, metadata, browser), means it is never collected or stored, so never billed, and session replay is opt-in.

_Enforced · Forge, on any infrastructure_

**Use:** `observability = forge.Observability { enabled = False }`, or one category off.

**Evidence:**

- skill `observability` (`forge skill load observability`)
- source [`kcl/schema.k`](../kcl/schema.k)

<a id="hosted-otel-injection"></a>

### Telemetry wired with no keys

The platform injects the OTLP endpoint and a per-env telemetry token into every hosted pod, so logs, traces and metrics flow with nothing to configure.

_Enforced · Reliant hosting_

**Evidence:**

- `internal/operators/workload/otelinject.go` in the control-plane repo
- `internal/telemetrytoken` in the control-plane repo

<a id="monitoring-any-language"></a>

### Monitoring for any app

An app forge never built — any language, any CI, any infrastructure — sends its traces, logs and metrics to Reliant with the standard `OTEL_*` variables, and `forge obs` reads its issues, logs and release health.

_Enforced · Reliant hosting_

**Works in any repo** (no forge.yaml). **Start:** `forge login`, then `forge obs init prod`, and set the `OTEL_*` variables it prints in the app's environment. **Agent time:** ~15 min.

**Use:** `forge login`, then `forge obs init prod`: it registers the env, mints an ingest token and prints the `OTEL_*` variables.

**Why:** Adopting a platform usually starts with a rewrite. Monitoring needs none: one command, three environment variables, and the app's errors and deploy regressions are in front of the team and its agents.

**Evidence:**

- command `forge obs init`
- command `forge obs tokens`
- command `forge obs receivers`
- skill `observability/monitoring-only` (`forge skill load observability/monitoring-only`)
- source [`internal/cli/env_declaration.go`](../internal/cli/env_declaration.go)
- `proto/services/telemetry_token/v1/telemetry_token.proto` in the control-plane repo
- `internal/releasehealth/infer.go` in the control-plane repo

<a id="hosted-observability-store"></a>

### Hosted observability store

Hosted envs store logs, traces, metrics and profiles in the org's own ClickHouse scope, queried by the same `forge obs` verbs and console as local ClickStack, with quota and opt-out enforced at the source.

_Enforced · Reliant hosting_

**Evidence:**

- command `forge obs query`
- `internal/obstenant` in the control-plane repo
- `internal/obspolicy` in the control-plane repo
- `internal/obsqueryquota` in the control-plane repo
- `docs/observability/query-service.md` in the control-plane repo

<a id="managed-postgres-backup-status-alert"></a>

### Backup health you can see

Deploy status reports base-backup age, WAL archiving and the oldest restorable point separately, and a built-in alert pages when a database's backups are on and failing.

_Enforced · Reliant hosting_

**Evidence:**

- `proto/controlplane/v1/deploy.proto` in the control-plane repo
- `internal/alerting/mdb_backup.go` in the control-plane repo

<a id="database-dashboards-panels"></a>

### Live panels over your data

Opt a managed database into dashboards and the console runs live panels as a read-only role that sees only what your migrations GRANT it.

_Enforced · Reliant hosting_

**Evidence:**

- source [`kcl/schema.k`](../kcl/schema.k)
- `internal/dbpanel` in the control-plane repo

<a id="audit-activity"></a>

### Audit trail and activity stream

Every rpc writes a structured audit record, `pkg/audit` persists a queryable trail, and the activity stream is declared on the rpc and written by one interceptor, never by hand.

_Enforced · Forge, on any infrastructure_

**Use:** Declare `(forge.v1.method).activity` (verb, target, env) on a mutating rpc; `pkg/audit` serves a scoped `ListAuditEvents`.

**Evidence:**

- library [`pkg/audit`](../pkg/audit)
- source [`pkg/activity/interceptor.go`](../pkg/activity/interceptor.go)
- lint lane `forge lint --conventions`
- library [`pkg/activity`](../pkg/activity)
- skill `observability/audit-log` (`forge skill load observability/audit-log`)
- skill `activity` (`forge skill load activity`)

<a id="dora-metrics"></a>

### DORA metrics from the ledger

Change-failure rate and time to restore are computed from the release ledger and alert history, not hand-reported.

_Recorded · Reliant hosting_

**Evidence:**

- `internal/dora` in the control-plane repo
- `internal/handlers/delivery_insights` in the control-plane repo

---

<a id="agents"></a>

## Agents

Forge is built to make an LLM agent effective in a project, and several features exist only for that: its whole surface prints from the binary, its skills ship inside it and match the version in use, and a defect in forge is one command to report. When a skill and the binary disagree, the binary is right.

<a id="skills-in-the-binary"></a>

### Skills that match your forge

`forge skill list`, `load` and `search` serve 180+ skills built into the forge you run, so an agent never reads guidance written for another version.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** `forge skill load adopt`: what forge can do for this repo, with estimates to quote. **Agent time:** ~2 min.

**Use:** `forge skill load forge`

**Why:** A skill that disagrees with the project is reported as a forge bug.

**Evidence:**

- command `forge skill load`
- command `forge skill search`
- source [`internal/templates/project/skills`](../internal/templates/project/skills)
- skill `forge` (`forge skill load forge`)

<a id="mcp-server"></a>

### Built-in MCP server

`forge mcp` exposes forge on stdio to any MCP-capable harness, deploys and bug reports included, with no shell scraping.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** `claude mcp add forge -- npx -y @reliantlabs/forge mcp` (or any MCP client); then call the `benefits` tool. **Agent time:** ~2 min.

**Evidence:**

- command `forge mcp`
- source [`internal/cli/mcp_tools.go`](../internal/cli/mcp_tools.go)
- source [`internal/cli/mcp_deploy.go`](../internal/cli/mcp_deploy.go)

<a id="hosted-mcp"></a>

### Hosted MCP connector

A remote MCP server with nothing to install, https://api.reliantapi.com/forge/mcp: sign in with OAuth from claude.ai, Claude Desktop, ChatGPT, VS Code or Cursor and ask about monitoring (status, issues, logs, traces, release verdicts), releases and environments, record a deploy or start monitoring an app, with the same tool names and inputs as the local server and a console link on every answer.

_Enforced · Reliant harness_

**Works in any repo** (no forge.yaml). **Start:** Add `https://api.reliantapi.com/forge/mcp` as a custom connector and sign in; `forge skill load connect` has the steps per client. **Agent time:** ~2 min.

**Evidence:**

- source [`internal/cli/mcp_hosted.go`](../internal/cli/mcp_hosted.go)
- source [`cli/hosted_mcp.go`](../cli/hosted_mcp.go)
- skill `connect` (`forge skill load connect`)

<a id="capabilities-introspection"></a>

### Whole surface in one call

`forge project capabilities --json` enumerates every command, lint analyzer and marker from the binary itself, and `forge project annotations` the entity-authoring vocabulary, so neither can drift from the code.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** `forge project capabilities` in any directory. **Agent time:** ~2 min.

**Evidence:**

- command `forge project capabilities`
- command `forge project annotations`

<a id="shapes-map"></a>

### Find any symbol instantly

`forge project shapes` locates every rpc, message, table, handler, hook and store with file:line, and `project map` annotates ownership.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project shapes`
- command `forge project map`
- command `forge project graph`

<a id="libraries-introspection"></a>

### Library API without source diving

`forge project libraries <pkg>` prints every signature of a `forge/pkg` package, which `go doc` renders as `struct{ ... }`, so an agent adopts an existing utility instead of re-implementing it.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** In a Go module that requires `github.com/reliant-labs/forge`, `forge project libraries <pkg>`. **Agent time:** ~2 min.

**Evidence:**

- command `forge project libraries`

<a id="project-audit"></a>

### Project state snapshot

`forge project audit --json` reports services, rpcs, entities, drift, unwired stubs and ownership in one document.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project audit`
- command `forge project introspect handlers`
- library [`pkg/diagnostics`](../pkg/diagnostics)

<a id="machine-readable-json"></a>

### JSON everywhere, with next steps

`--json` on lint, obs, deploy plans and status emits documented schemas with the shared exit codes and `suggested[]` next commands an agent runs instead of paraphrasing.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge lint --json`
- source [`docs/mcp.md`](../docs/mcp.md)

<a id="report-bug"></a>

### Agents report framework bugs

`forge report-bug` sends a redacted defect report with evidence, deduplicated, and sends nothing unless invoked; `--dry-run` prints exactly what would go.

_Enforced · Forge, on any infrastructure_

**Works in any repo** (no forge.yaml). **Start:** `forge report-bug --dry-run …` prints exactly what would be sent. **Agent time:** ~2 min.

**Evidence:**

- command `forge report-bug`
- source [`internal/cli/report_bug.go`](../internal/cli/report_bug.go)
- library [`pkg/redact`](../pkg/redact)
- source [`docs/report-bug.md`](../docs/report-bug.md)

<a id="project-memory"></a>

### Agent memory file generated

`forge project memory` renders the framework guide every agent session loads, kept current with the pinned forge, and describes the project as it is: an adopted app gets the forge pieces it uses and its own test commands, never forge's greenfield layout.

_Enforced · Forge, on any infrastructure_

**Why:** Read off the tree and forge.yaml: a forge Go service gets the full Go guide; an adopted app keeps its language and layout, with sections only for what it opted into (deploy envs, monitoring, its migration tool, frontends) and the gate taken from its Taskfile, package.json scripts or Makefile; a static site gets the frontend guide.

**Evidence:**

- command `forge project memory`
- source [`internal/templates/project/reliant.md.tmpl`](../internal/templates/project/reliant.md.tmpl)
- source [`internal/cli/memory_surface.go`](../internal/cli/memory_surface.go)
- source [`internal/cli/testdata/memory`](../internal/cli/testdata/memory)

<a id="harness-pointers"></a>

### Works in Claude Code, Cursor

Forge writes thin pointers for Claude Code and Cursor that route an agent to `forge skill`, never copies of skill bodies that would go stale.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge skill list`

<a id="harness-none"></a>

### No agent files when unwanted

`harness: none` (`forge project new --harness none`) adopts forge without adding an agent memory file, harness pointer or MCP config to a repository that keeps its own.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- command `forge project new --harness`
- source [`internal/generator/harness.go`](../internal/generator/harness.go)
- skill `forge/greenfield` (`forge skill load forge/greenfield`)

<a id="predictable-layout"></a>

### Layout agents can predict

Every project shares one enforced layout (one service per proto file, a service's files in one directory, contracts beside their packages), so an agent knows where code goes.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- lint lane `forge lint --conventions`
- command `forge project features`

<a id="design-discipline"></a>

### Design asks for a brief

The `frontend/design` skill makes an agent get a product brief before styling and will not invent an aesthetic, and the scaffolded UI is labelled starter code.

_Enforced · Forge, on any infrastructure_

**Evidence:**

- skill `frontend` (`forge skill load frontend`)

<a id="llm-placement-guidance"></a>

### The LLM proposes, code decides

The `llm` skill puts agentic or judgement work in a Reliant workflow that calls the app's API, keeps money, limits and approvals as rules in the app, and never calls a model from the browser.

_Enforced · Forge, on any infrastructure_

**Why:** The rule is "the LLM proposes, code decides": an inline call's output is untrusted input.

**Evidence:**

- skill `llm` (`forge skill load llm`)

<a id="build-an-app-workflow"></a>

### Build an App workflow

A built-in workflow scopes the app with you, scaffolds it with forge, gets it green, fans out the implementation and verifies it in one review loop.

_Enforced · Reliant harness_

**Evidence:**

- `internal/workflow/builtin/forge-one-shot.yaml` in the reliant repo

<a id="move-to-forge-workflow"></a>

### Move to Forge workflow

A built-in workflow migrates an existing codebase into a forge project, gated on forge's exit codes with approval steps.

_Enforced · Reliant harness_

**Evidence:**

- `internal/workflow/builtin/forge-migrate.yaml` in the reliant repo

<a id="oncall-agent"></a>

### An on-call first responder

Every paging alert and new, regressed or spiking error becomes a GitHub issue investigated read-only, with severity, evidence, suspected cause and a proposed fix.

_Enforced · Reliant harness_

**Evidence:**

- `internal/workflow/builtin/autopilot-oncall.yaml` in the reliant repo
- `internal/handlers/autopilot_receiver_internal` in the control-plane repo

<a id="ai-pr-security-review"></a>

### AI code and security review

Built-in reviewers comment on every pull request: one finds defects (comment or request-changes, never approves), one threat-models the diff with file:line evidence and exploit sketches.

_Enforced · Reliant harness_

**Evidence:**

- `internal/workflow/builtin/autopilot-pr-review.yaml` in the reliant repo
- `internal/workflow/builtin/autopilot-security-review.yaml` in the reliant repo

<a id="agent-forge-deploy-tool"></a>

### Agents deploy with guardrails

Reliant agents get forge read tools plus a deploy tool that plans first, applies asynchronously and waits, the only cluster-mutating path, kept deliberately conservative.

_Enforced · Reliant harness_

**Evidence:**

- `internal/toolexec/daemonruntime/cmd_forge_deploy.go` in the reliant repo
- `internal/toolexec/daemonruntime/cmd_forge.go` in the reliant repo
- `internal/toolexec/daemonruntime/cmd_forge_promote.go` in the reliant repo
