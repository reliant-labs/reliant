---
name: db
description: Database work — SQL migrations as the single schema source of truth, projected by forge generate into entity structs, ORM, CRUD wiring, and frontend pages.
emit: both
---

# Database Work

## The key principle

**Migrations are the source of truth for your database schema.** Not API types, not ORM structs — the SQL files in `db/migrations/`. The API describes what crosses the wire; the schema describes what the domain stores. Different concerns, different clocks, so they evolve independently:

- **DB-only fields** — audit trails, internal state, denormalized caches, tombstones. Add via migration; no API change.
- **DB-only tables** — bookkeeping, junction tables, event logs, queue state. Create with a migration, query by hand; no API contract, no cross-stack change.

## Migration discipline

- **Migrations are append-only.** Never edit a merged migration — write a new one. Everyone else's local state assumes it is immutable.
- **Roll forward only — never write a down migration.** Recover with a new forward (hotfix) migration; write changes expand-then-contract. `forge lint` (`no-down-migration`) fails on a new down file — see `db/deploy-migrations`.
- **Keep seed data out of migrations.**
- **A destructive change is allowed per file, in the file** — `-- forge:allow-destructive` on a line of that migration; there is no forge.yaml allowlist. See `db/orm-and-safety`.
- **A migration never waits forever for a lock.** Every migration runs under `lock_timeout` (5s) and `statement_timeout` (30m), and a migration whose lock times out is rolled back and retried (5 attempts, growing wait). Override per file with `-- forge:lock-timeout 30s` / `-- forge:statement-timeout 2h` at the top of the file — a long backfill needs the second. See `db/migration-timeouts`.
- **A migration file runs as one transaction.** So `CREATE INDEX CONCURRENTLY` (and the like) must be its file's only statement, and an enum value a file adds cannot be used in that file. `forge lint` refuses both; see `db/orm-and-safety`.

<!-- @forge-only:start -->
## SQL is the schema language

There is no schema DSL and no proto annotation: `db/migrations/*.up.sql` is the **single source of truth**. `forge generate` applies every up-migration (lexical order) to an ephemeral postgres shadow database, introspects the resulting tables (columns, types, nullability, PKs, indexes), and projects:

- **Entity structs + ORM** — `internal/db/<entity>_orm.go`, mirroring the applied schema: `time.Time` for timestamp columns (never `timestamppb`), pointers for nullable columns, native slices for arrays.
- **CRUD wiring** — `internal/handlers/<svc>/handlers_crud_ops_gen.go` (beside the service's `service.go` and rpc methods), including `<entity>ToProto` / `<entity>FromProto` conversions to the service-proto wire message.
- **Frontend pages, nav, and mocks** for each entity.

`db/migrations/` and `db/queries/` are yours and forge never modifies them; `internal/db/<entity>_orm.go` is regenerated.

There is **no `database:` driver or directory setting**: a service project with `db/migrations/` has a postgres database. A hand-written data layer opts out of the generated ORM with `//forge:no-orm: <why>` in the `internal/db` package doc, or moves the projection beside it with `database.orm.package` — see `db/orm-and-safety`.

## What makes an entity

**The store follows the schema; CRUD wiring follows the wire.** Every applied
table gets an entity struct and `<Entity>Store`, rpcs or not — opt-outs,
naming and keys: `db/store`. CRUD handlers, pages and seed roots exist when
**both halves** exist: a service declares the CRUD RPCs (`Create<X>` /
`Get<X>` / `Update<X>` / `Delete<X>` / `List<Xs>`), and the applied schema has
the matching table (`db/store`, "Naming"). A CRUD RPC whose entity has a wire
message but no table, and no hand-written implementation, **fails `forge
generate`** by name.

## Starting a new entity

**The proto is the only place an entity is declared.** Write the message under a leading `// forge:entity` comment, then run bare `forge scaffold` (`--dry-run` plans; `--service <svc>` narrows): it births every marked message — missing CRUD quintet + owned migration pair — and generates, stubbing each custom RPC pb-through. The marker is consumed at birth only; once the table exists it is inert and evolution is a new migration. Envelope messages are refused even when marked.

There is no CLI field grammar (`forge scaffold entity <name> <field:type ...>` is refused, printing the equivalent message for you): everything an entity needs — enums, `optional` for nullable, `buf.validate` rules, `// forge:read-only` / `// forge:secret` / `// forge:append-only`, a same-row derived column (`// forge:generated <expr>`), and real foreign keys — can only be spelled in the message.

`forge project annotations --kind field --json` is the **authoritative annotation spec** — the proto→column mapping a birth applies, every marker, and the `buf.validate` rules with their db/zod effects. It derives from forge's own renderer, recognizers and descriptors, so trust it over any table, including this one. A rule it omits (`const`, `uuid`, CEL) is still wire-enforced, just not projected.

## Behavior by convention — real columns, no annotations

The columns ARE the declaration. The generators read these off the introspected schema:

| columns | behavior |
|---------|----------|
| `deleted_at` | soft delete: DELETE becomes UPDATE, reads filter `IS NULL`, `ListAll*` is the unfiltered variant; an archive stamp opts out: `db/soft-delete` |
| `created_at` + `updated_at` | ORM-managed timestamps — type-gated: time columns (`TIMESTAMPTZ` et al) or legacy `TEXT` count; anything else (epoch integers) stays plain schema |
| text columns | spanned by the generated list `search` filter |
| `GENERATED ALWAYS AS (…) STORED` | derived column: postgres maintains it, the ORM excludes it from writes, and the seeder skips it |

## Just write postgres

forge is postgres-pinned. Your tests **and** forge's shadow introspection apply `db/migrations/*.up.sql` **verbatim** to a real ephemeral postgres (no Docker — a cached embedded-postgres binary, or a server via `FORGE_TEST_POSTGRES_URL`). There is no portability subset to honor: anything postgres accepts works — `::type` casts, schema-qualified names, `JSONB`, native arrays, generated/identity columns.

Statements the bare ephemeral DB can't satisfy (`CREATE EXTENSION` for an uninstalled extension, a nonexistent role) are skipped by the shadow — they can't affect the table/column model. A failing `CREATE TABLE` / `ALTER TABLE` / `DROP TABLE` / `CREATE INDEX` is a **hard `forge generate` error**, since skipping one would generate an ORM that lies.

## Evolving an entity

Write a migration; the projections follow. Data movement is plain SQL in the same file.

```sql
-- db/migrations/20260115093042_add_bookmark_rating.up.sql
ALTER TABLE bookmarks ADD COLUMN rating BIGINT NOT NULL DEFAULT 0;
UPDATE bookmarks SET rating = 5 WHERE done;
```

```bash
forge generate   # struct, ORM, conversions, pages all pick up the column
```

Wire evolution stays proto: service-proto messages are the **API truth** and evolve independently after the one-time scaffold. The generated conversions map the **intersection** of wire fields and columns by name — wire-only fields never reach the DB, column-only fields never reach the wire.

## Forge DB commands

Each takes `--dsn "$DATABASE_URL"`:

```
forge db migration new <name>      # create an empty migration (up only)
forge db migration rebase <file>   # re-version one the schema already passed
forge db migrate up                # apply pending migrations
forge db migrate status            # show what's applied
forge db migrate force <version>   # clear a dirty migration state (runs no SQL)
forge db introspect                # show live schema
task dev-psql                      # interactive shell (no --dsn)
```

Versions are **UTC timestamps**; never hand-type one. See **`db/versioning`**.
A release whose migration a target env's schema has already passed is refused
at the cut and the deploy — **`db/release-guard`**.

### Recovering a wedged dev database

Never hand-edit `schema_migrations` or `DROP DATABASE` — measured, two runs did exactly that, having missed the commands above. Match the symptom:

| Symptom | Do this |
|---|---|
| `migration N failed part-way and is marked dirty` — nothing will migrate until the flag clears, and `forge db migrate up` refuses too | repair the schema, then `forge db migrate force N`, then `forge db migrate up` |
| `applied version N is behind latest on disk M` — nothing is broken | `forge db migrate up` |
| dev rows are stale, wrong, or hand-mangled | `forge db seed reset` |

Read `db/recovery` before `forge db migrate force`: it asserts a version without verifying it.

Dev DSN convention: `postgres://postgres:postgres@localhost:5432/<project>?sslmode=disable`

Deployed envs migrate themselves — load the `db/deploy-migrations` skill.

To read an env's rows, `forge db query <env> "<sql>"` (read-only, no `forge:secret` columns): load `db/query`.

`forge db seed apply|status|reset` materializes deterministic dev rows from the applied schema, and `db/seeds/vocab.yaml` teaches it your domain's vocabulary — load the `db/seeding` skill before seeding.

## Where the rest lives

| You need… | Load |
|---|---|
| birthing one named entity, the birth markers, the proto → SQL column mapping | `db/birth` |
| write policy (ownership, `forge:immutable`, `forge:version`), append-only tables, two FK routes to one parent, CHECKs mirroring a Go set, derived values, status-lifecycle CHECKs | `db/constraints` |
| what the generated ORM does, overriding generated CRUD, where custom queries go | `db/generated-orm` |
| which tables get a store, naming, keys | `db/store` |
| destructive changes, `//forge:no-orm`, ORM field types | `db/orm-and-safety` |
| dashboard panels over this database | `db/dashboards` |

## Forge-specific rules

- **Draft, then `forge generate` — it is the fast, exact oracle.** Let `forge scaffold entity` write the first-draft migration + proto (or hand-write one), then generate, read the error, fix, repeat. A failing `CREATE TABLE`/`ALTER TABLE`, a missing import, or a List filter naming no real column fails loudly. Don't reverse-engineer forge's `internal/**` or exhaustively plan first.
- **The schema drives the struct, never the reverse.** Need a field on the entity struct? Write the migration that adds the column. Don't hand-edit `gen/` or `internal/db/<entity>_orm.go` — those are the only DB-layer files generate rewrites.
- **Open the serving pool with `serverkit.OpenDB`** (the scaffolded `openDatabase` does), not `sql.Open`. Besides the bounded connect retry, its `pgx` pool rides through a pooler rollout: a statement a draining PgBouncer refused (`FATAL: server shutting down`, 08P01 — it never ran) is run again on another connection, and closing a connection the pooler already dropped is not an error. To reach the `*pgx.Conn` through `sql.Conn.Raw`, assert `interface{ Conn() *pgx.Conn }`, not `*stdlib.Conn`.

See also: `proto` for the wire half (CRUD RPC naming), `architecture` for the naming-conventions table.
<!-- @forge-only:end -->
