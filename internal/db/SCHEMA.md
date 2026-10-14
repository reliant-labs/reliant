# Database Schema Management

## Overview

Reliant runs exclusively on **Postgres**. The schema is **forge migrations**:
forward-only SQL files under `db/migrations/` (repo root), embedded into the
binary by `forge generate` (`db/embed_gen.go`) and applied by forge's migrator
(`forge/pkg/migratekit`, golang-migrate underneath). `sqlc` reads the same
files to generate type-safe Go code, until the forge ORM replaces it (P5b in
`research/forge-migration/PLAN.md`).

## Files

- **`db/migrations/`** - Forge migrations, `<YYYYMMDDHHMMSS>_<name>.up.sql` (source of truth for the schema). The first is the baseline that replaced goose's history.
- **`postgres/queries/`** - Hand-written SQL queries compiled by `sqlc`
- **`sqlc.yaml`** (repo root) - Points `sqlc` at `db/migrations/` and `postgres/queries/`
- **`migrate.go`** - `Migrate` (apply), `WaitForSchema` (wait for the api-server to apply), and the read-only state behind `reliant db migrate status`

## Workflow

### Adding a New Migration

1. **Create and edit the migration**:

   ```bash
   make migration NAME=my_changes
   # Edit db/migrations/<version>_my_changes.up.sql
   ```

   `make migration` runs `forge db migration new` from the forge go.mod pins
   and starts the file declared backward-compatible (below). Always let forge
   pick the version: it allocates a UTC timestamp that sorts after every
   existing one. There is no down migration — a bad migration is repaired by a
   new one written against the state the database is actually in.

   A migration file runs as ONE transaction (golang-migrate sends it as a
   single multi-statement query). A statement that cannot run in a transaction
   block, such as `CREATE INDEX CONCURRENTLY`, must be the only statement in
   its file — and do not write one yet: while forge's migrator holds its run
   lock, the other api-server replica booting beside it waits in a blocking
   `pg_advisory_lock`, which holds a snapshot that `CREATE INDEX CONCURRENTLY`
   waits for, and Postgres breaks the deadlock by failing the waiter: every
   replica but the one migrating fails its boot with `deadlock detected`
   (reproduced against forge `8d812662`; reported to forge. The goose bridge
   polls for its own lock for this reason).

   **Say whether the previous release survives it.** Rolling back is
   redeploying the previous release onto the schema this one left — until a
   release after the cutover ships, that is the last goose release. Every
   migration after the baseline carries one of:

   ```sql
   -- forge:backward-compatible — additive column; the previous release never reads it
   -- reliant:breaks-rollback drops chats.title, which the previous release selects
   ```

   The first is the default and the expand half of expand/contract (add in one
   release, stop reading the old shape, drop it in a later one). forge's
   migrator records it, so an older forge release can prove it may start on
   the newer schema. The second gives the rollback up on purpose; the runbook
   changes with it. `TestForgeMigrationsDeclareCompatibility` fails a migration
   that says neither, or still carries `make migration`'s placeholder.

2. **Regenerate Go code**:

   ```bash
   make sqlc
   ```

3. **Write your Go code** using the newly generated types.

4. **Run and test** — the api-server applies pending migrations on startup
   (`reliant db migrate up` does the same as a one-shot), and DB-backed tests
   build their template database the same way.

5. **Commit everything** (migration + generated code + your Go code).

### When a migration lands out of order

golang-migrate applies only versions above the one the database records, so a
migration whose version sorts below one already deployed would never run; the
migrator refuses that state rather than skip it. If your branch's migration is
older than one that merged first, re-version it before merging:

```bash
forge db migration rebase db/migrations/<version>_my_changes.up.sql
```

## Applying and inspecting

| Command | Description |
| ------- | ----------- |
| `reliant db migrate up` | Apply every pending migration (DATABASE_URL, or DATABASE_DIRECT_URL when set) |
| `reliant db migrate status` | Read-only (one READ ONLY transaction): what `up` would do — up to date, apply N, build fresh, adopt, goose catch-up then adopt, or refuse and why. Exits non-zero on a refusal |
| `reliant db migrate force <version>` | Clear a dirty schema after a hand repair (records the version, runs no SQL) |
| `make sqlc` / `make db-regenerate` | Generate Go code from `db/migrations/` + `postgres/queries/` |

## goose history (frozen) and the goose bridge

Until 2026-10 the schema was goose migrations in `migrations/postgres/`. They
are frozen, byte for byte (`TestGooseHistoryIsFrozen`): a file added or edited
there fails the build. The baseline in `db/migrations/` is their squashed
result and lists every goose version in its `-- forge:adopts goose` lines
(`TestBaselineAdoptsExactlyTheGooseHistory` keeps the list and the directory
equal). Three kinds of database meet it, through the same `Migrate`:

- **fresh** (no `goose_db_version`): the baseline's SQL runs;
- **at goose's frozen head**: **adopted** — the baseline is recorded as
  applied in forge's bookkeeping (`schema_migrations*`), none of its SQL runs,
  and `goose_db_version` is left exactly as it was;
- **behind goose's head** (an older goose release migrated it): the **goose
  bridge** (`goose_bridge.go`) first runs goose itself — the same `goose.Up`
  every goose release ran at boot, over these frozen files — to goose's head,
  then the migrator adopts it, in the same run.

A goose version the frozen history does not hold, a frozen version recorded as
rolled back, or reliant's tables with no history at all are refused before
anything runs. `reliant db migrate status` says which of these a database is.
The goose files and library stay compiled in for the bridge; when they can go
is in the cutover runbook, `research/forge-migration/04-p5a-cutover-runbook.md`.
