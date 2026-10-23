---
name: db
description: Database work — SQL migrations are the single schema source of truth; forge generate projects the applied schema into entity structs, ORM, CRUD wiring, and frontend pages.
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

## Birthing one named entity

Bare `forge scaffold` births every marked message. To birth one — or to birth a message whose CRUD quintet already exists but whose table does not — name it:

```bash
forge scaffold entity order --from-proto tasks
forge scaffold entity --from-proto tasks.Order
forge scaffold entity --from-proto tasks         # batch: quintets with no applied table
```

The `--from-proto <svc>` batch form sweeps `// forge:entity`-marked messages too. Birth is **one-time**: the migration is yours the moment it is written, and forge never re-reads the proto to regenerate or edit it.

**There is no import path** (`--from-schema` is refused). To put a wire contract on a table that already exists, hand-write the matching proto message and run `forge generate` — it projects the ORM and CRUD wiring from the applied schema without writing a migration.

### Birth markers — schema hardening baked into your owned migration

Everything a marker adds lands in the **user-owned birth migration**, never a forge-owned `_gen` file. Entity-level markers tablize the message, so a marked message needs no separate `// forge:entity` line. Run `forge project annotations --kind entity` (and `--kind field`) for the full list, placement and effect. The storage-side nuances:

- **`// forge:append-only`** — a real **DB trigger** in the birth migration raises on any `UPDATE`/`DELETE`, so no bug or compromised caller can rewrite history. The CRUD quintet completes to Create/Get/List only.
- **`// forge:secret`** — stripped from reads, and withheld from the read-only `forge_ro` role by Postgres; credential columns need a decision: `db/query`.
- **`// forge:read-only`** — the input-side mirror: readable but not client-writable (status, computed price, a lifecycle timestamp). The born Create omits it; the generated Update preserves it on a full replace and refuses an `update_mask` naming it (every `forge generate`; vs `forge:immutable`: `db/write-policy`). Your RPCs write it via `db.Update<Entity>Masked`; give the column a DB `DEFAULT` or the first insert lands the zero. Mark it up front: the born `new/page.tsx`, edit form and `handlers_crud_test.go` leave it out.
- **`// forge:generated <expr>`** — births `<col> <type> NOT NULL GENERATED ALWAYS AS (<expr>) STORED`, the rest of the comment line copied verbatim (NOT NULL unless `optional`; implies `forge:read-only`). Birth tries it against the shadow database first and refuses, at the marker's file and line, an expression postgres rejects. See `db/derived-values`.
- **`// forge:soft-delete`** — **OPT-IN**: unmarked entities get no `deleted_at` and hard-delete. Beyond the read filter, `ListAll<Entities>` returns tombstones. The `--soft-delete` flag and a message already carrying a `deleted_at` field do the same thing.

Proto → SQL, in both birth forms: scalars to `NOT NULL` columns with zero defaults, `optional` to nullable, enums to `TEXT` + `CHECK (col IN (...))` (DEFAULT = first non-`_UNSPECIFIED` member), repeated scalars to arrays, maps/nested messages to `JSONB`, an `*_id` string whose stem resolves to a real entity to `TEXT` + an applied `REFERENCES` and index, and a `// forge:generated <expr>` field to the same type with `GENERATED ALWAYS AS (<expr>) STORED` in place of its DEFAULT. oneof/`Any`/cross-package fields become TODO comment lines, never silently dropped. Envelopes — Request/Response messages, anything carrying `page_size`/`page_token`/`update_mask` — are refused. An enum over an `INTEGER` column you declared (any width, or `INTEGER[]`) also projects: it stores the wire number, and a `CHECK (col IN (1, 2, …))` gives the seeder its vocabulary.

### Column write policy — ownership, immutability, concurrency

Whether a full-replace UPDATE may touch a column is schema truth, declared as a
`COMMENT ON COLUMN` in the migration that owns it and introspected by `forge
generate`. Three decisions live there, all in `db/write-policy`:

- **Row ownership** — forge stores none of its own. If rows belong to a user, an
  org or an account, that is a column you write in the birth migration; adding
  it later is a migration plus a backfill.
- **`forge:immutable`** — keeps a full-replace Update from zeroing a column the
  request never carried (a server-assigned owner id, a Stripe customer ID, a
  creation stamp).
- **`forge:version`** — opt-in optimistic concurrency. Without it the last
  writer wins silently.

Table policy: `COMMENT ON TABLE payments IS 'forge:append-only';` makes a
ledger immutable in both stores and free-function delegates: no Update/Delete.
The birth marker writes the trigger AND comment; on later generates, applied
schema (not the marker) decides. Contradictory RPCs are refused. Correct a row
by appending its reversal. Load `forge skill load db/append-only` for the
compile-time surface and recognition of existing guard triggers.

### Two routes to the same parent

When a table reaches the same parent two ways — `estimates.customer_id`
directly, and `estimates.job_id → jobs.customer_id` — both foreign keys are
individually valid and the two routes can still name different rows. Declare
which one wins with a `COMMENT ON CONSTRAINT ... 'forge:ref ...'` in a
migration, the same `COMMENT ON` vocabulary as `forge:immutable` above on a
constraint instead of a column. `forge db seed` refuses to write rows where an
undeclared pair disagrees. The vocabulary, the sweep query that finds every
diamond at once, and which route to pick are in `db/seeding`.

### A CHECK that mirrors a Go set

A CHECK listing a vocabulary Go also owns (access-token scopes) declares it:
`COMMENT ON CONSTRAINT … IS 'forge:closed-set <set>'`, and `forge lint` keeps
the two equal. Set names and the fix migration: `db/closed-sets`.

## Behavior by convention — real columns, no annotations

The columns ARE the declaration. The generators read these off the introspected schema:

| columns | behavior |
|---------|----------|
| `deleted_at` | soft delete: DELETE becomes UPDATE, reads filter `IS NULL`, `ListAll*` is the unfiltered variant; an archive stamp opts out: `db/soft-delete` |
| `created_at` + `updated_at` | ORM-managed timestamps — type-gated: time columns (`TIMESTAMPTZ` et al) or legacy `TEXT` count; anything else (epoch integers) stays plain schema |
| text columns | spanned by the generated list `search` filter |
| `GENERATED ALWAYS AS (…) STORED` | derived column: postgres maintains it, the ORM excludes it from writes, and the seeder skips it |

### A derived value is generated, or written by its owner

Derived from the SAME row (`line_total = quantity * unit_price`) → a `GENERATED ALWAYS AS (…) STORED` column, declared at birth with `// forge:generated <expr>`; never a plain column plus a CHECK asserting the equality, which nothing ever satisfies. Derived from OTHER rows (`amount_paid` summing `payments`) → a plain column marked `// forge:computed`, written by the RPC that owns the change. Load `db/derived-values` for the postgres rules a generated expression must meet (birth checks them) and worked examples.

### A status-lifecycle rule is an implication, not a biconditional

"An approved estimate has an approval timestamp" seeds cleanly one way only:

```sql
-- YES: one-way implication. Any number of these over `status` seed fine.
CONSTRAINT estimates_approved_has_stamp
    CHECK (status <> 'ESTIMATE_STATUS_APPROVED' OR approved_at IS NOT NULL)

-- NO: biconditional. `forge db seed apply` cannot place it, nor any guard on
-- `status` beside it, and apply is one transaction: the whole dev dataset.
CONSTRAINT estimates_approved_iff
    CHECK ((status = 'ESTIMATE_STATUS_APPROVED') = (approved_at IS NOT NULL))
```

Enforce the mirror half (not approved ⇒ NULL stamp) in the RPC that owns the
transition. Why, and the mixing rule: `db/seeding`.

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

## Generated ORM semantics

- **Field types are Go's, as wide as the column** (`ID`, `int32`, `json.RawMessage`, `forge:go-type`): `db/orm-and-safety`.

- `Create<Entity>` is a plain `INSERT`, never an upsert. A key the schema fills (serial, identity, any `DEFAULT`) is left to the database when zero and read back via `RETURNING`; a set key is inserted as set. A key with no default is the caller's (an empty string key gets `ulid.Make()`).
- Writes scan database-computed values back via `RETURNING` — generated columns on every write, DB defaults on insert, unwritten columns on update — so never re-read a row you just wrote.
- **A `NOT NULL` column's `DEFAULT` applies to an unset field**, and an explicit zero is still written: `db/write-policy`.
- With stampable `created_at` / `updated_at` columns, Create/Upsert stamp each one left unset and keep one you set (`db/write-policy`); Update re-stamps `updated_at`; `created_at` is immutable on update. Stamps use the column's projected type: time columns get `time.Now().UTC()`, legacy `TEXT` columns get RFC3339Nano text, nullable columns are stamped through their pointer.
- `internal/db/*_orm.go` (and `orm_shared.go`) are Tier-1 self-certifying: each carries an embedded `forge:hash` marker, so hand-edits trip the drift guard in any clone or worktree. `forge project disown internal/db/<entity>_orm.go --reason ...` is the sanctioned one-way exit.
- Each entity exports `<Entity>Columns`, the declared-column allowlist. `forge/pkg/crud` validates user-supplied `order_by` against it; an undeclared column is `InvalidArgument`, not a silent no-op.
- Each entity also exports `<Entity>Constraint<Name>` for every named UNIQUE / CHECK / FOREIGN KEY on its table — the value **postgres** reports in a violation, including the names it auto-derives for inline declarations (`UNIQUE` on `jobs.estimate_id` → `jobs_estimate_id_key`) that appear nowhere in your migration text. Branch on them with `orm.ConstraintName(err)` rather than a hand-written string, so renaming a constraint in a migration is a compile error instead of a branch that stops matching. No constant for the primary key: `pkg/crud` generates the id, so nothing branches on a duplicate-PK insert. See `service-layer` for the classification switch.
- **Statements are sent with their values bound** — generated CRUD, bun builders and `NewRaw` alike — so each is one prepared statement per shape under pgx's default statement cache (keep it; `default_query_exec_mode=exec` re-plans every statement). An integer `?` argument binds only through `orm.Arg` (`orm.ArgList` for `IN`); `orm.Literal` keeps a value the planner must see in the text: `db/crud-overrides`.
- Every by-key method (`Get…ByID`/`…ByPK`, `Update`, `UpdateMasked`, `Delete…`) answers a missing row with `svcerr.NotFound("<entity>")`, also `errors.Is(err, orm.ErrNoRows)` — return or `svcerr.Wrap` it, never re-derive it. Other repo errors → `Internal`, no SQL on the wire.
- A delegate or store called with `RunTx(ctx, orm.Serializable, fn)`'s `ctx` joins it (no `FOR UPDATE` helpers; no default level): `service-layer/transactions`.
- The delegates above are free functions, so `internal/db/store_gen.go` also exports them as INTERFACES: `<Entity>Store` per entity plus an aggregate `Store`. That is what a service's `Deps` field names — **never hand-write an interface plus a passthrough adapter over the ORM**; forge generates both and asserts they match. Depend on the narrowest one that works.
- **To see what a store offers, read the interface, not forge:** `go doc ./internal/db <Entity>Store` prints the full method set, and `forge project shapes --kind store` lists every one with its `file:line`. Not forge's generator source — it describes every project, not your schema; a measured run lost 11 turns there.

## Diverging from generated CRUD

`handlers_crud_ops_gen.go` is Tier-1 and keeps regenerating. To customize one RPC — or to attach a per-caller policy to one — override an exported op field in your owned `handlers_crud.go` instead of disowning the ops file. Load the `db/crud-overrides` skill for the seams.

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

### Queries

Every query goes through ONE data-access path: the generated store, then the owned seam beside it. forge does not run sqlc and scaffolds no `db/queries/`. A second, parallel query layer would re-describe the schema the ORM already projects. A project porting off sqlc keeps it running meanwhile as a `forge.yaml` generate hook (`generate.hooks`, see `architecture`), and deletes the hook when the last query has moved.

- Generated: `go doc ./internal/db <Entity>Store` covers reads by key, filtered lists (`orm.QueryOption`) and writes.
- Anything else: `internal/db/<entity>_repo_ext.go`, the custom-query seam forge scaffolds beside every entity, once, and never rewrites. Its header names the tools: the generated delegates with `orm.QueryOption` filters, SQL via `db.Bun()`, and the `orm.Context` that runs a method in or out of a transaction. bun takes joins, CTEs (`NewSelect().With(...)`), `FOR UPDATE SKIP LOCKED`, advisory locks and fully raw statements (`NewRaw`), so a hand-tuned hot-path query stays hand-tuned SQL.
- Test every `_repo_ext.go` query against a real migrated database (`testing/integration`). That is what checks the SQL against the schema; nothing does it at generate time.
- Bucketing by time has a silent timezone trap: load `db/time-buckets`.

## Forge-specific rules

- **Draft, then `forge generate` — it is the fast, exact oracle.** Let `forge scaffold entity` write the first-draft migration + proto (or hand-write one), then generate, read the error, fix, repeat. A failing `CREATE TABLE`/`ALTER TABLE`, a missing import, or a List filter naming no real column fails loudly. Don't reverse-engineer forge's `internal/**` or exhaustively plan first.
- **The schema drives the struct, never the reverse.** Need a field on the entity struct? Write the migration that adds the column. Don't hand-edit `gen/` or `internal/db/<entity>_orm.go` — those are the only DB-layer files generate rewrites.
- **Open the serving pool with `serverkit.OpenDB`** (the scaffolded `openDatabase` does), not `sql.Open`. Besides the bounded connect retry, its `pgx` pool rides through a pooler rollout: a statement a draining PgBouncer refused (`FATAL: server shutting down`, 08P01 — it never ran) is run again on another connection, and closing a connection the pooler already dropped is not an error. To reach the `*pgx.Conn` through `sql.Conn.Raw`, assert `interface{ Conn() *pgx.Conn }`, not `*stdlib.Conn`.

See also: `proto` for the wire half (CRUD RPC naming), `architecture` for the naming-conventions table.
<!-- @forge-only:end -->
