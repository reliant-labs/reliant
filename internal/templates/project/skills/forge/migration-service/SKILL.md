---
name: migration-service
description: Migrate a server-shaped project to forge — services, operators, workers, webhooks, packs, multi-binary cmd/, k8s manifests.
---

# Migrate a Server-Shaped Project

Use this skill when the existing project is network-facing: HTTP/gRPC servers, background workers, webhook receivers, k8s deployments. For CLI / library shapes see `migration-cli`. For prerequisites and the overall flow see `migration`.

## Scaffold

```bash
forge project new <name>-next \
  --kind service \
  --mod github.com/<owner>/<name>-next \
  --service <main-service> \
  --frontend <main-fe>
```

`--kind service` is the default; emitting it documents intent. Pass `--service`/`--frontend` for the primary network-facing components; add the rest one at a time after scaffold so each is verified in isolation.

## Add components, one at a time

```bash
forge scaffold service <name>      # additional Connect-RPC services
forge scaffold operator <name>     # k8s operator / controller loop
forge scaffold worker <name>       # background worker (Start/Stop lifecycle)
forge scaffold worker <name> --kind cron --schedule "..."   # cron worker
forge scaffold webhook <name> --service <existing-service>  # webhook on an existing service
```

Run `forge generate && go build ./...` after each. Hyphens are OK: forge stores the hyphenated form as the display name and snake-cases the directory and Go package paths.

## Services whose handlers already exist: `// forge:unmanaged`

Adopting forge **in place** on a server that already has its protos and handlers (see `migration`, "Adopting in place")? Before the first `forge generate`, put `// forge:unmanaged` above every `service X {` whose handlers live outside `internal/handlers/`. Generate then scaffolds nothing for those services (no stubs, mocks, mounts, compose entries or subcommands) while buf still generates their code, and `forge project audit` lists them as the adoption ledger.

Your existing server keeps mounting them by hand. Port them one per PR:

1. Delete the service's marker and run `forge generate` → `internal/handlers/<svc>/` with a stub per rpc.
2. Move each rpc's logic onto its `*Service` method; return `svcerr.Wrap(err)` rather than hand-built connect errors.
3. In the existing server, replace the old handler's mount with the forge one; delete the old handler.
4. `forge lint` now holds that service to forge's conventions (e.g. `forgeconv-list-filter-optional`), which it skipped while the service was unmanaged.

The boot-time completeness gate (`RequireComplete`) checks only the forge-managed services in `app.Inventory`, so unmanaged services never block a forge-served binary from starting — even one that shares a proto file with a managed service.

## Cross-cutting concerns are code + libraries, not packs

Auth, audit logging, API keys, and frontend components are owned scaffold you edit or thin code over a `forge/pkg/*` library — there is nothing to `install`. The parent **`migration`** skill covers each (which file owns it, which library backs it, how the interceptors compose) and applies unchanged here.

One rule is specific to server-shaped projects:

**Forge deliberately does NOT ship a NATS / Kafka / generic-queue integration, nor a bundled Stripe / Twilio client.** Wire-format conventions (subject naming, message envelopes, retry/DLQ shape) and third-party SDK surfaces are too project-specific. Install the SDK directly (`github.com/nats-io/nats.go`, `github.com/stripe/stripe-go`, `github.com/segmentio/kafka-go`, RabbitMQ, ...) and write a thin wrapper under `internal/<name>/` with a `contract.go` exposing your actual surface. See `adapter` for the wrapper shape.

## Known migration landmines

All surfaced in real migrations — spot the symptom, apply the fix:

- **audit: two interceptors.** The `forge/pkg/audit`-backed DB interceptor and the scaffold's slog-only `middleware.AuditInterceptor` record the same events — wire ONE in `internal/app/compose.go` (`NewComponents`), never both.
- **webhook proto package.** A webhook's proto generates as `db.v1`, NOT `<project>.db.v1`. Proto package names align with the buf module root, not the project name. Templates that try to prefix the project name will lint-fail.
- **Multiple webhooks on the same service** once redeclared shared symbols (`webhookMaxBodySize`, `webhookEvent`, `extractEventID`, `verifyHMACSHA256`); fixed — templates now emit unique names per webhook.

## Green baseline before porting business logic

```bash
forge generate
go mod tidy
go build ./...
forge lint
```

All four must pass on the empty scaffold. Fix failures here, not after porting.

## Set the contracts floor

Before porting any internal package, edit `forge.yaml`:

```yaml
contracts:
  strict: true
  allow_exported_vars: false
  allow_exported_funcs: false
  exclude: []
```

`forge lint --contract` then joins the per-phase gate. See `contracts` for the per-package pattern (interface in `contract.go` first, `forge generate`, then implementation behind it).

## Porting order

1. **Internal utility packages first** (domain types, naming, validation helpers) — fewest deps on the rest of the codebase.
2. **Database layer** (`db/migrations/` plus any hand-written query files). Migrations are the schema source of truth — copy them as-is; don't port the source repo's generated ORM or entity types. `forge generate` shadow-applies them and projects the entity structs/ORM into `internal/db/<entity>_orm.go` for every table that also has CRUD RPCs in a service proto. Write plain postgres DDL (see the `db` skill) — the shadow applies migrations verbatim to a real ephemeral postgres; auxiliary DDL the bare DB can't satisfy is skipped.
3. **Services** (`internal/handlers/<svc>/`). One co-located directory per service: hand-written `contract.go` + impl alongside the generated `handlers_crud_ops_gen.go` CRUD projection. The `*_gen.go` files are rewritten on every `forge generate`; only your hand-written code (the `contract.go` interface, its implementation, domain types) moves over. There is no separate top-level `handlers/<svc>/` tier — collapse any source split into the one `internal/handlers/<svc>/`.
4. **Composition** (`internal/app/compose.go` `NewComponents`, off the owned `internal/app/providers.go` `Infra`/`OpenInfra`). `NewComponents(infra *Infra) (*Components, error)` constructs every component inline in type-topological order and fills each component's interface-typed `Deps` off `infra.<Field>`, resolved BY TYPE. `providers.go` is yours, scaffolded once; `compose.go` is yours too — forge writes it once and then reconciles only the component set and the Deps literal's keys, so late-bound/two-phase construct-then-inject setters you add there are kept (`forge project disown internal/app/compose.go` to stop reconciliation entirely). Port wiring intent by declaring each collaborator on `Infra` and filling the `Deps` fields in `NewComponents`; there is no `bootstrap.go` / `wire_gen.go` name-matched layer to wire things for you.
5. **Workers, operators, webhooks** — under `internal/workers/<name>/` and `internal/operators/<name>/` (webhooks attach to their service under `internal/handlers/<svc>/`); implement the lifecycle methods (`Start`, `Stop`, `Reconcile`, webhook event handlers). Constructed in `NewComponents`, surfaced through `WorkerList`/`OperatorList` over `*Components` in the owned `internal/app/lifecycle.go` (forge appends an accessor for each newly discovered worker/operator).

## Port-time design decisions you should NOT defer

A 1-for-1 port is the goal, but some source patterns are smells forge's defaults surface. Fixing them at port time is cheap; later it is a refactor across every caller.

### Wide repository facades — split, don't exclude

A single wide `Repository` (a "god DAO") trips `interfacebloat`. The lint is correct; do NOT path-exclude it. Two clean options:

- **Drop the wide interface entirely.** Codebases with a 100+ method `Repository` usually have narrow per-aggregate interfaces (`UserRepository`, `OrgRepository`, `BillingRepository`) beside it; the concrete `*PostgresRepository` satisfies all of them structurally. Each caller depends on the narrow interface it needs, so no individual interface is over the limit.
- **Split the package.** `internal/db/user/`, `internal/db/org/`, `internal/db/billing/`, each with its own `contract.go` + narrow `Service`. Larger refactor; cleaner long-term.

**Source uses sqlc (or another query generator)?** forge does not run sqlc, so port those queries rather than carrying the generator over. Map each table to the generated `<Entity>Store`, and each query the store cannot express to `internal/db/<entity>_repo_ext.go` (bun or raw SQL, tested against a real database; see `db`). Mid-port, the carried-over generated `Queries` struct trips `interfacebloat` because its shape is not yours to split. Path-exclude the generated dir with a note naming the port that removes it ("generated by sqlc; deleted when <aggregate> moves to the ORM"). For hand-written DAOs, split.

**No `Service` alias needed when you split.** No name-matched generator assumes every package exposes a `Service`. The composition resolves deps by type, not by name: a consumer that needs `audit.Repository` is filled from whatever concrete on the `Infra` provider set structurally satisfies it (`*db.PostgresRepository` satisfies `audit.Repository` or the code does not compile). Drop the umbrella `type Service interface { ... }` — it only ever existed to feed the deleted generators.

### Adding a dep is a compile-time edit to the composition

Adding or removing a collaborator means editing the component's `Deps` struct (`internal/handlers/<svc>/service.go` for a service, `internal/<name>/contract.go` for a domain package) and ensuring the matching field exists on the owned `Infra` provider set so `NewComponents` can fill it by type. Both are caught by the Go compiler (or surface as a `forge generate` "no provider" report if `Infra` lacks the type). If a port phase drops a vestigial `Logger` field from `<pkg>.Deps`, regen stops filling it and `go build` points straight at any stale reference.

### Goose → golang-migrate

If the source uses goose (one-file migrations with `-- +goose Up` / `-- +goose Down` markers), forge expects golang-migrate's forward-only shape (one `.up.sql` per migration — forge writes, runs and accepts no down migrations; `forge lint` fails on a new `.down.sql`). `forge project migrate import --from goose` does the conversion; by hand it is mechanical:

1. Keep the `-- +goose Up` section as `<N>_<name>.up.sql` and **discard** the `-- +goose Down` section (recovery is a new forward migration, never a reverse).
2. Drop `-- +goose StatementBegin` / `-- +goose StatementEnd` markers (they wrap single statements; golang-migrate handles that natively).
3. Split a file declaring `-- +goose NO TRANSACTION` into one migration per statement, cut where goose cuts them (a line ending in `;`, or a `StatementBegin`/`StatementEnd` block). goose ran each of those statements on its own. golang-migrate has no no-transaction mode: it sends a file as one multi-statement query, which postgres runs as one implicit transaction. Kept whole, a file with two `CREATE INDEX CONCURRENTLY` statements fails with SQLSTATE 25001 and leaves the schema dirty, and so does an `ALTER TYPE … ADD VALUE` followed by a row using the value (55P04). The import names each piece `<name>_part<N>`.
4. Renumber files starting from the next-available index AFTER any pack-installed migrations (e.g. audit-log occupies 00002, api-key occupies 00003, so source migrations start at 00004).

If source migrations have foreign-key dependencies on tables that pack migrations create (or vice versa), reorder carefully. Pack migrations are not negotiable; renumber yours.

**Renumbering is only for databases you can rebuild.** A database goose already migrated would run every renumbered file again. To keep those databases, squash goose's history into one first-migration baseline that declares `-- forge:adopts goose <every goose version>`. The migrator then adopts each one on its next run without re-running anything. See `db/deploy-migrations`, "Adopting a database another tool migrated".

## Test regressions during port — fix the port, never blame the source

If a test passes in the source repo but fails in the port, **it is always a port bug**. Never write "pre-existing in source" in a synthesis or friction note without first running `go test ./<same-package>/...` against the source tree to verify.

Before declaring a migration complete, the synthesizing agent MUST:

1. Run `go test -count=1 ./...` in the ported tree, capture the failing test names.
2. For each failing test, run the equivalent in the source tree (paths usually differ by `internal/service/<x>/` vs `internal/<x>/`).
3. If source passes and the port fails → port bug. Fix or revert that package; never ship a report with red tests.
4. If both fail → can be flagged as inherited, but only with the source-test exit code captured in the synthesis output.

## Lint failures during port — fix the code or `//nolint:`, never path-exclude

When `forge lint` fires on a freshly-ported package, **do NOT** add `internal/<pkg>/` to a path-based exclusion list in `.golangci.yml` — that silences every linter on the package, including the bug-catchers (errcheck, govet, staticcheck, unused).

In order of preference:

1. **Fix the code.** Most `gocognit` / `funlen` / `nestif` flags point at a function that benefits from being split; `interfacebloat` points at a god-interface (see above). Take the small refactor.
2. **`//nolint:gocognit // ported as-is from <source-path>; rewriting risks behavior drift` at the function declaration.** Per-line, with a justification comment.
3. **Path exclusion as a LAST resort, on generated code only.** Things like `gen/`, `internal/<pkg>/embed.go`, or a carried-over query generator's output dir awaiting its port. Never on hand-written code.

A clean port lands with at most a handful of `//nolint:` annotations, not a growing list of yaml path exclusions.

## Multi-binary `cmd/` layouts

`cmd/` is entrypoints only: one cobra root (`cmd/main.go`) plus one per-command subcommand file (`cmd/<svc>.go`, `cmd/<binary>.go`), each driving the serve path (`app.OpenInfra` → `app.NewComponents` → the generated `(*Components).Mount<Svc>` onto `serverkit.Server` → `serverkit.Run`). `forge scaffold service` adds the subcommand + its `internal/handlers/<svc>/`. For source binaries that aren't first-class forge components:

- Wrapping a forge-managed service or worker: prefer `forge scaffold` and let forge own the wiring.
- Genuinely standalone (proxies, sidecars, off-service consumers) needing their own Deployment: `forge scaffold binary <name>` — a peer subcommand with its own composition (`OpenInfra` → `NewComponents`). See the `binaries` skill.
- Tiny one-off scripts that don't deserve a contract.go (dev seed, one-shot migration helper): a thin `cmd/<name>.go` subcommand on the root, no `internal/<name>/` package.

## k8s manifests

Forge emits `deploy/kcl/<env>/` (KCL-based manifests, one dir per environment: `dev`, `staging`, `prod`). KCL is canonical — there is no "ship hand-written YAML instead" mode. Either:

- **Adopt KCL** (recommended). Translate hand-written manifest customizations into KCL overrides; declare each process as an `fw.Workload` in `deploy/kcl/workloads.k` and bind it per env (`wl.x | {runtime = forge.OnCluster {target = _cluster}}`); raw manifest dicts that are not workloads (ClusterIssuers, SealedSecrets, hand-typed CRDs) go in `manifests = [forge.Manifests {objects = [...]}]` on the Bundle.

`forge.yaml` stays strictly top-level (project identity, the version pin, a few real settings). Per-env config (logging, env vars) lives in `deploy/kcl/<env>/` alongside the per-env deploy knobs (cluster/namespace/registry/domain) on the env's `forge.ClusterTarget` — there is no second `config.<env>.yaml` format. The single typed config struct (`internal/config`, generated from proto config blocks) serves server, CLI, and standalone binaries via cmdkit; non-server binaries do NOT hand-roll `os.Getenv`/ad-hoc loggers/hardcoded timeouts.

## Final checks

```bash
forge generate          # idempotent on a healthy project
forge lint              # contract + db + general lints
forge build             # binaries + frontends + Docker images
task test              # unit + frontends (`task test:all` adds integration)
task test:e2e          # full-stack (requires `forge env up dev` in another shell)
forge env deploy dev        # local k3d
```

## Rules

- One service per proto package.
- Add a component before the code that extends it (e.g. the service before its webhook receiver), then `forge generate`. Never the reverse.
- KCL or hand-rolled manifests, not both.

## When this skill is not enough

- **CLI / library shape** — see `migration-cli`.
- **Contract surface** for ported internal packages — see `contracts`.
- **Auth / audit / third-party integrations** — see `auth` (auth + API keys) and `adapter` (thin wrappers over SDKs like Stripe/Twilio/NATS).
