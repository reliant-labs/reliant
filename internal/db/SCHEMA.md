# Database Schema Management

## Overview

Reliant runs exclusively on **Postgres**. Migrations under `migrations/postgres/`
are the source of truth for the schema, and `postgres/schema.sql` is the schema
snapshot that `sqlc` reads to generate type-safe Go code.

## Files

- **`migrations/postgres/`** - Goose migration files (source of truth for the schema)
- **`postgres/schema.sql`** - Schema consumed by `sqlc`, generated from the migrations by `scripts/generate-schema-sql.sh`
- **`postgres/queries/`** - Hand-written SQL queries compiled by `sqlc`
- **`sqlc.yaml`** - Points `sqlc` at `postgres/schema.sql` and `postgres/queries/`

## Workflow

### Adding a New Migration

1. **Create and edit the migration**:

   ```bash
   goose -dir internal/db/migrations/postgres create my_changes sql
   # Edit the migration file with your schema changes
   ```

2. **Regenerate `postgres/schema.sql`** from the migrations:

   ```bash
   scripts/generate-schema-sql.sh
   ```

   This applies every migration to a disposable Postgres and dumps the result,
   so the file cannot describe a schema the migrations do not build. Do not edit
   it by hand — `TestSchemaSQLMatchesMigrations` fails when the two disagree.

3. **Regenerate Go code**:

   ```bash
   make sqlc
   ```

4. **Write your Go code** using the newly generated types.

5. **Run and test** — migrations auto-apply on startup:

   ```bash
   go run ./cmd/reliant
   ```

6. **Commit everything** (migration + schema.sql + generated code + your Go code).

## Make Targets

| Command              | Description                            |
| -------------------- | -------------------------------------- |
| `scripts/generate-schema-sql.sh` | Regenerate `postgres/schema.sql` from the migrations |
| `make sqlc`          | Generate Go code from `postgres/schema.sql` |
| `make db-regenerate` | Alias for `make sqlc`                  |

## Migration Numbering

Migrations are numbered by timestamp prefix. CI (`.github/workflows/check-migrations.yml`)
validates that new migrations come after `main`'s highest number. If a rebase
introduces a conflict, `scripts/fix-migration-conflicts.sh` renumbers the offending
migrations; the `post-rebase` git hook (install via `scripts/install-git-hooks.sh`)
runs it automatically.
