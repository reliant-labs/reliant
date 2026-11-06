---
name: db/generated-orm
description: What the generated ORM does — Go field types, plain INSERTs with RETURNING, defaults and managed timestamps, drift-guarded _orm.go files, column and constraint allowlists, bound statements, NotFound on by-key methods, transactions, the <Entity>Store interfaces — plus overriding generated CRUD and the one data-access path for custom queries (_repo_ext.go).
---

# The generated ORM

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
- The delegates above are free functions, so forge exports them as INTERFACES: `<Entity>Store` (`<entity>_store_gen.go`) plus a `Store` aggregate. That is what a service's `Deps` field names — **never hand-write an interface plus a passthrough adapter over the ORM**; forge generates both and asserts they match. Depend on the narrowest one that works.
- **To see what a store offers, read the interface, not forge:** `go doc ./internal/db <Entity>Store` prints the full method set, and `forge project shapes --kind store` lists every one with its `file:line`. Not forge's generator source — it describes every project, not your schema; a measured run lost 11 turns there.

## Diverging from generated CRUD

`handlers_crud_ops_gen.go` is Tier-1 and keeps regenerating. To customize one RPC — or to attach a per-caller policy to one — override an exported op field in your owned `handlers_crud.go` instead of disowning the ops file. Load the `db/crud-overrides` skill for the seams.

## Queries

Every query goes through ONE data-access path: the generated store, then the owned seam beside it. forge does not run sqlc and scaffolds no `db/queries/`. A second, parallel query layer would re-describe the schema the ORM already projects. A project porting off sqlc keeps it running meanwhile as a `forge.yaml` generate hook (`generate.hooks`, see `architecture`), and deletes the hook when the last query has moved.

- Generated: `go doc ./internal/db <Entity>Store` covers reads by key, filtered lists (`orm.QueryOption`) and writes.
- Anything else: `internal/db/<entity>_repo_ext.go`, the custom-query seam forge scaffolds beside every entity, once, and never rewrites. Its header names the tools: the generated delegates with `orm.QueryOption` filters, SQL via `db.Bun()`, and the `orm.Context` that runs a method in or out of a transaction. bun takes joins, CTEs (`NewSelect().With(...)`), `FOR UPDATE SKIP LOCKED`, advisory locks and fully raw statements (`NewRaw`), so a hand-tuned hot-path query stays hand-tuned SQL.
- Test every `_repo_ext.go` query against a real migrated database (`testing/integration`). That is what checks the SQL against the schema; nothing does it at generate time.
- Bucketing by time has a silent timezone trap: load `db/time-buckets`.
- Dashboard panels over this database: `db/dashboards`.
