# Keep your migration tool: `database.migrations`

forge's schema features — the ORM and stores projected from the applied
schema, seeding, frontend mocks, migration safety, `forge db migrate` — used to
need your migrations in forge's own shape (`db/migrations/<timestamp>_<name>.up.sql`,
applied by forge's migrator). They no longer do. Declare where your migrations
live and which tool applies them, and forge runs that tool wherever it would
have applied them itself.

```yaml
# forge.yaml
database:
  migrations:
    dir: migrations # default db/migrations; `database.migrations_dir` is the shorthand
    tool: goose # forge (default) | goose | atlas | flyway | dbmate | golang-migrate | custom
    # apply: [...]    # override the tool's default argv; required for custom
    # status: [...]
```

Nothing changes for a project that sets neither key.

## What forge runs, and where

| Where                                                                                               | What                                                                                                                                                            |
| --------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `forge generate` and every schema reader (ORM, seeds, mocks, fixtures, lint lanes that execute SQL) | `apply` against a throwaway Postgres database, then introspects it. Cached per run on a digest of the directory's files and the command.                        |
| `forge db migrate up`                                                                               | `apply` against `--dsn` / `$DATABASE_URL`                                                                                                                       |
| `forge db migrate status` / `version`                                                               | `status`                                                                                                                                                        |
| `forge db reset` (dev)                                                                              | drops and recreates the database, then `apply`                                                                                                                  |
| `forge lint --migration-safety`, `forge ci migration-safety`                                        | the SQL-text rules over every `.sql` file the tool applies (goose and dbmate: the Up section); forge-only rules are reported as `skipped (tool: <tool>): <why>` |

The command is an argv with no shell, run in the project root with
`DATABASE_URL`, `FORGE_DATABASE_URL_NO_PASSWORD` and `PGPASSWORD` (the URL
without its password, and the password), `FORGE_MIGRATIONS_DIR`,
`FORGE_PROJECT_DIR`, `FORGE_PHASE` (`shadow` or `cli`), plus the variables the
built-in tool reads natively (`GOOSE_DBSTRING`, `FLYWAY_URL`, …). `${NAME}` in
an argument expands one of those; nothing else is expanded. No preset puts the
password on an argv.

## What forge stops doing

`forge db migration new`, `migration rebase`, `migrate force` and `squash`
refuse and name the tool's equivalent; `forge scaffold` does not write a birth
migration into the tool's directory; the binary embeds no migrations (so
`AUTO_MIGRATE` is a no-op); the release migration-order guards become notes.

## Deploying

The migrate job in `deploy/kcl/workloads.k` is yours: point it at an image that
holds the tool **and** the migrations (a two-line Dockerfile `FROM <tool image>`

- `COPY <dir> /migrations`), or, on the host runtime,
  `command = ["forge", "db", "migrate", "up"]`.

Per-tool examples and the details: `forge skill load db/own-migration-tool`.
