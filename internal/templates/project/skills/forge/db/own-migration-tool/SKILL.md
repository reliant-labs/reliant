---
name: own-migration-tool
description: Keep your migration tool — goose, atlas, flyway, dbmate, golang-migrate or your own command — and your migrations directory, and still get forge's ORM projection, seeding, migration safety and `forge db migrate`. The forge.yaml `database.migrations` hook, per-tool examples, what forge runs and with which environment, what changes (and what does not), and how to migrate on deploy.
applies: any
---

# Keep your migration tool

You do not have to move your migrations onto forge's migrator to get what forge
does with a schema. Point forge at your directory, name your tool, and forge
runs **your tool** wherever it would have applied migrations itself.

```yaml
# forge.yaml
database:
  migrations:
    dir: migrations        # your directory (default db/migrations)
    tool: goose            # forge | goose | atlas | flyway | dbmate | golang-migrate | custom
```

That is the whole adoption for most teams: **about 10 minutes**, and nothing in
your migrations changes.

| Step | Time |
|---|---|
| add the two keys above | 1 min |
| `forge generate` — your tool migrates a throwaway Postgres, forge projects the ORM from it | 2–5 min |
| `forge lint --migration-safety` — read the findings and the "skipped (tool: …)" lines | 2 min |
| (deploying with forge) declare the migrate job with your tool's image | 10–20 min |

The same declaration lives on the database in the env (KCL) instead, and must
when a project has several databases — `driver` is `tool`:

```python
schema_only_databases = [forge.Database {
    name = "default"
    migrations = forge.DatabaseMigrations {dir = "migrations", driver = "goose"}
}]
```

`apply`, `status` and `check` take the same argv there. Declare it in ONE place:
forge.yaml's `database.migrations` and a KCL `migrations` together are refused.
`forge db … --db <name>` runs the named database's tool. Load `db` (Declaring a
database's schema in the env) for the rules.

## What you get

- **The ORM, stores and CRUD wiring from your schema.** forge applies your
  migrations — by running your tool — to an ephemeral Postgres and introspects
  the result. Nothing parses your SQL dialect or file names, so goose
  annotations, flyway repeatables and atlas checksums all just work.
- **Seeding** (`forge db seed apply`) and every other reader of the applied
  schema: frontend mocks, test factories, CRUD fixtures. Seeding writes only to
  a fully migrated database, and your tool decides what that means (below).
- **Migration safety** for every `.sql` file your tool applies: destructive
  statements, `ADD COLUMN … NOT NULL` without a default, `SET NOT NULL` without a
  backfill, volatile defaults, and (where your tool wraps a file in a
  transaction) `CREATE INDEX CONCURRENTLY` beside other statements and an enum
  value used in the file that adds it. Rules about forge's own file naming,
  roll-forward policy and `-- forge:backward-compatible` declarations are
  reported as `skipped (tool: goose): <why>` — never run against files they do
  not describe, and never silently passed.
- **`forge db migrate up` / `status` / `version`** run your tool against the
  database (`--dsn`, else `$DATABASE_URL`). `forge db reset` re-migrates with it.
- **No protos needed.** With a hook declared, migration safety and the schema
  readers work in a repo with no `.proto` files and no forge codegen.

## Per-tool examples

Each built-in tool has a default `apply` and `status`. Override either with an
argv (no shell; `[sh, -c, "..."]` for a pipeline).

```yaml
# goose — one file per migration, `-- +goose Up` / `-- +goose Down`
database:
  migrations: {dir: migrations, tool: goose}
# runs: goose -dir <dir> up    (status: … status); the URL comes from GOOSE_DRIVER/GOOSE_DBSTRING

# golang-migrate — NNN_name.up.sql / .down.sql
database:
  migrations: {dir: db/migrations, tool: golang-migrate}
# runs: migrate -path <dir> -database <URL without password> up   (status: … version); password via PGPASSWORD

# dbmate — `-- migrate:up` / `-- migrate:down`
database:
  migrations: {dir: db/migrations, tool: dbmate}
# runs: dbmate --migrations-dir <dir> --no-dump-schema migrate   (status: … status)

# atlas — versioned migrations with atlas.sum
database:
  migrations: {dir: migrations, tool: atlas}
# runs: atlas migrate apply --dir file://<dir> --url <URL without password>  (status: atlas migrate status …); password via PGPASSWORD

# flyway — V1__init.sql, R__views.sql
database:
  migrations: {dir: sql, tool: flyway}
# runs: flyway migrate   (status: flyway info), with FLYWAY_URL/_USER/_PASSWORD/_LOCATIONS set

# anything else — a Makefile target, a Rails/Django/Prisma command, a script
database:
  migrations:
    dir: prisma/migrations
    tool: custom
    apply: [npx, prisma, migrate, deploy]
    status: [npx, prisma, migrate, status]
    check: [npx, prisma, migrate, status]  # exits non-zero when one is pending or failed
```

**The password never reaches an argv.** Any user on the machine can read a
process's arguments. goose, dbmate and flyway read their connection from the
environment forge sets, so their presets pass no URL. golang-migrate and atlas
take the database only as a flag, so their presets pass
`${FORGE_DATABASE_URL_NO_PASSWORD}` and the password travels as `PGPASSWORD`,
which their postgres drivers read. Your own `apply`/`status` should do the
same: name `${FORGE_DATABASE_URL_NO_PASSWORD}`, never `${DATABASE_URL}`.

## The contract

forge runs the argv in the project root, with:

| Variable | Value |
|---|---|
| `DATABASE_URL` | the database to migrate (a `postgres://` URL) |
| `FORGE_DATABASE_URL_NO_PASSWORD`, `PGPASSWORD` | the same URL without its password, and the password (set when the URL has one) |
| `FORGE_MIGRATIONS_DIR` | the migrations directory, absolute |
| `FORGE_PROJECT_DIR` | the project root, absolute |
| `FORGE_PHASE` | `shadow` (the throwaway schema database) or `cli` (`forge db migrate`) |
| goose | `GOOSE_DRIVER=postgres`, `GOOSE_DBSTRING`, `GOOSE_MIGRATION_DIR` |
| dbmate | `DBMATE_MIGRATIONS_DIR`, `DBMATE_NO_DUMP_SCHEMA=true` |
| flyway | `FLYWAY_URL` (JDBC), `FLYWAY_USER`, `FLYWAY_PASSWORD`, `FLYWAY_LOCATIONS=filesystem:<dir>` |

`${NAME}` in an argument is replaced by one of those variables; nothing else is
expanded (a `$` in a password survives). Exit 0 means applied. `status` output
is printed as your tool wrote it; forge does not parse it. The tool must be on
`PATH` — a missing one fails with its name.

Your tool's history table — `goose_db_version`, `flyway_schema_history`,
`atlas_schema_revisions`, `schema_migrations`, or the one your argv names with
`-table`, `--migrations-table`, `x-migrations-table` or `--revisions-schema`,
schema-qualified and quoted as postgres reads them — is its bookkeeping: the
ORM never projects it, and `forge db seed` never plans
or resets it.

**Seeding asks your tool whether the database is fully migrated.** A matching
schema is not proof: a pending data-only migration, or one that failed after
its DDL, leaves the tables looking current. So before `forge db seed apply` or
`reset` writes, forge reads a built-in tool's history table in the target and
in the shadow it just migrated: every migration the shadow records must be
recorded in the target (a flyway repeatable, `R__*.sql`, at the same checksum,
since flyway re-runs one whose content changed), none may be marked failed
(golang-migrate's `dirty`, a flyway migration whose last run did not succeed,
an atlas revision with an error or unapplied statements), and none may have
been applied from content other than the file's (flyway's checksum, atlas's
hash).
A custom tool's history is unknown to forge, so it declares
`check:`, an argv that exits 0 only when every migration is applied and none
failed; without one, seeding a custom-tool project is refused. A declared
`check:` answers for a built-in tool too. The target must also hold every
table and column the migrations create.

The shadow apply is cached per run on a digest of every file in the directory
plus the apply command, so a `forge generate` applies your migrations once.

## What does not change, and what forge stops doing

- Your files, names and numbering are yours. forge never writes into the
  directory: `forge db migration new`, `migration rebase`, `migrate force` and
  `squash` refuse and name your tool's equivalent (`goose create …`,
  `dbmate new …`). `forge scaffold` births no table either: create it with your
  tool, and the entity's CRUD wiring follows once it is in the applied schema.
- The project binary embeds no migrations, so `AUTO_MIGRATE` and
  `<binary> db migrate up` do nothing; migrate with your tool.
- The release migration-order guards (`db/release-guard`) are notes, not
  refusals: they judge forge's version order, and your tool orders by its own.

## Migrating on deploy

The migrate step is an ordinary job workload in `deploy/kcl/workloads.k`, and it
is yours. Swap forge's (`args = ["db", "migrate", "up"]` on the project binary)
for your tool:

```python
fw.Workload {
    name = "migrate"
    kind = "job"
    before = [fw.BEFORE_ALL]
    # The image must hold BOTH the tool and the migrations: a stock tool image
    # has no migrations in it. Build one from a two-line Dockerfile:
    #   FROM <an image with your tool: migrate/migrate, arigaio/atlas,
    #         flyway/flyway, ghcr.io/amacneil/dbmate, or one you build goose into>
    #   COPY migrations /migrations
    build = fw.DockerBuild {dockerfile = "deploy/migrate.Dockerfile"}
    command = ["goose", "-dir", "/migrations", "up"]
    env = {GOOSE_DRIVER = "postgres", GOOSE_DBSTRING = fw.DatabaseRef {name = "main"}}
}
```

On the host runtime (`forge env up dev`) a workload with `command` runs that
argv on your machine, so `command = ["forge", "db", "migrate", "up"]` runs your
hook against the env's `DATABASE_URL` with the tool you already have installed.
`before = [fw.BEFORE_ALL]` keeps every other workload waiting for it, exactly as
forge's own job did. See `db/deploy-migrations` for the ordering and failure
behaviour, which do not depend on the tool.

## Moving onto forge's migrator later

Nothing forces it. When you want forge's run lock, adoption, compat
declarations and release guards, `forge project migrate import --from goose`
(or `forge db squash --from-dsn`) writes a forge baseline that adopts your
history; then delete `tool:`.
