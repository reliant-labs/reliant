# Database Schema Management

## Overview

This project uses **migrations as the single source of truth** for the database schema. The `schema.sql` file is automatically generated from migrations and should **never be edited manually**.

## Files

- **`migrations/sqlite/`** - SQLite migration files (source of truth for sqlite)
- **`migrations/postgres/`** - Postgres Goose migration set (selected when `DATABASE_DRIVER=postgres`)
- **`schema.sql`** - Auto-generated from migrations (DO NOT EDIT)
- **`sqlc.yaml`** - Points to `schema.sql` for code generation

## Workflow

### Adding a New Migration

#### Standard Workflow (Generate First)

1. **Create and edit migration**:

   ```bash
   goose -dir internal/db/migrations/sqlite create my_changes sql
   # Edit the migration file with your schema changes
   ```

2. **Regenerate schema + code**:

   ```bash
   make db-regenerate
   ```

   This generates `schema.sql` and Go types, allowing you to write code using the new types.

3. **Write your Go code** using the newly generated types

4. **Run and test**:

   ```bash
   go run ./cmd/reliant
   ```

   Migrations auto-apply on startup.

5. **Commit everything** (migration + schema.sql + generated code + your Go code)

#### Alternative: Code-First Workflow

If you prefer to write Go code first without waiting for schema regeneration:

1. **Create migration + manually update schema.sql** with your changes

2. **Regenerate sqlc only**:

   ```bash
   make sqlc
   ```

3. **Write and test your Go code**

4. **Before committing, regenerate schema from migrations** to ensure consistency:
   ```bash
   make schema-generate
   ```
   If this produces different output, your manual schema.sql edit was incorrect.

**Recommendation**: Use the standard workflow - regenerating first ensures correctness.

### Validation

The CI pipeline automatically validates that `schema.sql` matches the migrations:

```bash
make schema-validate
```

If out of sync, regenerate with:

```bash
make schema-generate
```

## Make Targets

| Command                | Description                             |
| ---------------------- | --------------------------------------- |
| `make schema-generate` | Regenerate `schema.sql` from migrations |
| `make schema-validate` | Check if `schema.sql` is in sync        |
| `make db-regenerate`   | Regenerate both schema and sqlc code    |
| `make sqlc`            | Generate Go code from `schema.sql`      |

## How It Works

1. **Migrations are applied** to a temporary database
2. **Schema is extracted** using SQLite's `.schema` command
3. **`schema.sql` is generated** from the extracted schema
4. **sqlc reads `schema.sql`** to generate type-safe Go code

## Benefits

✅ **Single source of truth** - Migrations define the schema
✅ **No synchronization bugs** - schema.sql is always derived from migrations
✅ **CI validation** - Prevents schema drift
✅ **Automated** - No manual schema file maintenance

## Migration Best Practices

1. **Always test migrations** on a copy of your database first
2. **Include rollback** (down migration) for reversibility
3. **Regenerate schema.sql** after every migration change
4. **Never edit schema.sql directly** - it will be overwritten

## Troubleshooting

**Problem**: `schema.sql` out of sync error in CI

**Solution**:

```bash
make schema-generate
git add internal/db/schema.sql
git commit -m "Regenerate schema.sql"
```

**Problem**: sqlc generates incorrect types

**Solution**: Regenerate both schema and sqlc:

```bash
make db-regenerate
```
