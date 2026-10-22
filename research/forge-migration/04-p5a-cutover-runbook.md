# 04 — P5a cutover runbook: goose → forge migratekit

> **Status: complete, and goose is gone.** Prod's `reliant_cp` was adopted on
> 2026-10-19 and goose was removed on 2026-10-22 (§9). This file is the record
> of how the cutover was done; nothing in it is still to run. The live rules
> are `internal/db/SCHEMA.md`.

The pre-flight, rollout and rollback steps (old §5–§8) and the CI workflow
edit (old §10) were run and are deleted; git history has them.

Revised 2026-10-14 for the user's condition on approving P5a: *"Let's not break
prior users or the prod db."* Against forge `8d812662` (reliant #885), on
throwaway Postgres 16. The implementation was two reliant branches, merged back
to back (#912, #914):

| Branch | What | Changes a deployed DB? |
|---|---|---|
| `feat/p5a-forge-migrations-baseline-r2` | `db/migrations/20261014050404_baseline.up.sql` (goose's 156 migrations, squashed by `forge db squash --from-dsn`, with its `-- forge:adopts goose` list); `db/embed_gen.go`; tests that freeze goose's history by version AND content | No — nothing runs forge's migrator yet |
| `feat/p5a-migrate-up-migratekit-r2` | `db.Migrate`: the goose bridge, then `migratekit.Apply`; `db migrate status` as a read-only preview; `force`; the backward-compatible rule; sqlc reads `db/migrations` | **Yes — this is the cutover** |

Removing goose was not part of the cutover; §9 records when and how it went.

## What this revision changes

1. **Older goose databases are upgraded, not refused (the goose bridge).** The
   previous design refused any database not at goose's exact head and told the
   operator to run the last goose release first. That breaks every self-hosted
   or otherwise stale install. Now the new binary still embeds goose's frozen
   history and the goose library, and `db.Migrate` first runs goose itself —
   the same `RunMigrations` (`goose.Up` with `WithAllowMissing`) every goose
   release ran at boot, over the same files — to bring a database that is
   behind to goose's head, then lets forge's migrator adopt it, in one run.
2. **`db migrate status` states exactly what `up` will do**, from the same
   code the bridge decides with (`db.PlanMigration`), in one `READ ONLY`
   transaction: `up to date` / `apply N` / `fresh database` / `adopt` /
   `goose catch-up N, then adopt` / `refuse: <why>`. Proven read-only by full
   `pg_dump` before and after (§4).
3. **Every forge migration after the baseline must say whether the previous
   release survives it** (`-- forge:backward-compatible — <why>`, or
   deliberately `-- forge:breaks-rollback <why>`), enforced by
   `forge lint --migration-safety` (forge.yaml's
   `database.migration_safety.require_compat_declaration`); `make migration`
   writes both as a comment to replace. That keeps "redeploy the last goose
   release" a working rollback.
4. The baseline was re-cut for `20261013215013_worktree_failure_reason` (#861),
   `20261014055046_integration_writes` (#893) and
   `20261014142537_trigger_event_origin` (#909): **156** goose migrations,
   head `20261014142537`, cut from a database main `111d67a5` built. It is now `forge db squash
   --from-dsn` output end to end (header, adoption list and body), and
   re-running the squash writes the same bytes.
5. `db migrate up` prints what happened from `migratekit.Apply`'s `Result` and
   the bridge's report; the stale "a Migrator holds a second session" notes are
   gone (forge #706 made every migratekit path one session).

## 1. How the baseline was cut

The goose database must be built by **the last goose release** — the code path
that built prod — not by a goose CLI.

```bash
docker run -d --name reliant-baseline-cut -e POSTGRES_PASSWORD=cut -p 127.0.0.1::5432 postgres:16-alpine   # prod is POSTGRES_16
PORT=$(docker port reliant-baseline-cut 5432/tcp | cut -d: -f2)
docker exec reliant-baseline-cut psql -U postgres -c 'CREATE DATABASE goose_built'
go build -o /tmp/reliant-goose ./cmd/reliant          # on main, before the cutover merges
DATABASE_URL="postgres://postgres:cut@127.0.0.1:$PORT/goose_built?sslmode=disable" /tmp/reliant-goose db migrate up

V=$(forge db migration new baseline | grep -o '[0-9]\{14\}'); rm db/migrations/${V}_baseline.up.sql
forge db squash --from-dsn "postgres://postgres:cut@127.0.0.1:$PORT/goose_built?sslmode=disable" --to ${V}_baseline
forge generate                                        # first time only: writes db/embed_gen.go, db/source_gen.go
docker rm -fv reliant-baseline-cut
```

Use the pinned forge for every `forge` here
(`go run github.com/reliant-labs/forge/cmd/forge@<forge.yaml forge_version>`).
Squash reads `goose_db_version` for the adoption list and leaves goose's table
and sequence out of the body. Do not edit the file.

The baseline's `-- forge:adopts goose` lines were removed with goose (§9):
every database we keep had been adopted by then, and migratekit reads the
declaration only on a database it has no record in.

## 2. What the cutover binary does

`db.Migrate(ctx, dsn, logger)` (`internal/db/migrate.go`) is the one migration
path: `server api` (`MigrateApply`) and `reliant db migrate up` both call it
with `MigrationURL()` (`DATABASE_DIRECT_URL` when set, else `DATABASE_URL`). It
opens a one-connection pool — one Postgres session for the whole run — and:

| The database | What happens |
|---|---|
| fresh (no `goose_db_version`, no `schema_migrations`) | the baseline's SQL runs |
| goose at its frozen head (exactly the 156 versions) | **adopted**: the baseline is recorded in `schema_migrations`, `schema_migrations_applied`, `schema_migrations_compat`; none of its SQL runs; `goose_db_version` untouched |
| goose behind its head (a subset of the 156) | **goose catch-up, then adopt**: goose applies the missing versions (writing its own `goose_db_version` rows, as the next goose release would have), then the database is adopted |
| a goose version the frozen history does not hold | refused before anything runs: `NOTHING WAS APPLIED` |
| a frozen version whose latest goose row is a rollback | refused before anything runs (goose would never re-run it) |
| reliant's tables, but neither tool's history | refused before anything runs |
| already adopted / built | forge's migrator: pending forge migrations, or nothing. `goose_db_version` is never read again |
| dirty `schema_migrations` | refused; `reliant db migrate force <v>` after a hand repair |
| ahead of this binary | starts only if every newer version was declared `-- forge:backward-compatible` |

- **Bookkeeping.** Adoption writes only forge's three `schema_migrations*`
  tables. No reliant table has such a name (checked against the baseline).
  `goose_db_version` is written only by goose itself during a catch-up, and
  never once the database is at goose's head.
- **Locks and concurrency.** Replicas and a `db migrate up` Job may run at
  once. The catch-up is serialized by a session advisory lock
  (`gooseBridgeLockKey`, `"reliant\x02"`) on the run's one session, and every
  waiter re-reads the database under it. Waiters **poll**
  `pg_try_advisory_lock`: a session blocked in `pg_advisory_lock` holds a
  snapshot, goose's history has `CREATE INDEX CONCURRENTLY` migrations that
  wait for every older snapshot, and Postgres breaks that deadlock by
  cancelling one side (reproduced; the blocking version failed the concurrent
  test with `deadlock detected`). migratekit's run lock then serializes
  adoption and forge migrations.
- **Waiters.** `WaitForSchema` (worker, gateway) polls `schema_migrations`:
  current when clean and at or past the newest embedded version
  (`RELIANT_DB_SCHEMA_WAIT_SECONDS`, 300 s). A long catch-up on a very old
  self-hosted database can approach that; the waiter exits and its supervisor
  restarts it.
- **Commands.** Unchanged: `reliant db migrate up`, no flags, `DATABASE_URL`,
  `DATABASE_DRIVER`, `DATABASE_DIRECT_URL`. New: `db migrate force <v>`.
  `db migrate status` now prints a verdict and exits 1 when `up` would refuse.

## 3. Forge migrations after the baseline

- `make migration NAME=x` runs the pinned `forge db migration new`, which
  opens the file with both declarations as a comment to replace. forge.yaml
  sets `database.migration_safety.require_compat_declaration`, so
  `forge lint --migration-safety` (a blocking step in PR CI) fails a
  migration after the baseline that declares neither
  `-- forge:backward-compatible — <why>` nor `-- forge:breaks-rollback <why>`,
  gives no reason (a `<why …>` placeholder is none), or declares both.
- Why it matters: rolling back is redeploying the previous release onto the
  schema the new one left: an older forge release, whose migrator refuses to
  start on a schema ahead of it unless every newer version was declared
  compatible. Expand in one release, contract in a later one.
- A `-- forge:breaks-rollback` migration means that, from that release on,
  rollback is a forward fix.
- `CREATE INDEX CONCURRENTLY` must be the only statement in its file
  (`forge lint --migration-safety` enforces it). With forge `8d812662`,
  migratekit's run-lock waiters blocked in `pg_advisory_lock`, holding a
  snapshot the index build waited for: 4 concurrent runners over
  `CREATE TABLE` + a `CREATE INDEX CONCURRENTLY` migration, 3 trials, and
  every time the 3 waiters failed `take the migration run lock: ERROR:
  deadlock detected (SQLSTATE 40P01)`. forge #776 (pinned since `f3fd8c02`)
  makes the waiters poll `pg_try_advisory_lock`, as the goose bridge does, so
  the statement is allowed again.

## 4. Proof (throwaway Postgres 16.15; all passed)

The matrix below ran on the 155-version cut. The re-cut for #909 (156
versions) was checked at merge time: a database the last goose release
(main `111d67a5`) built and a fresh one the baseline built have identical
normalized `pg_dump --schema-only`, and the in-process `TestMigrate_*` matrix
passed again against it.

Every goose database was built by goose: (b) by the last goose release's
binary (main `25d88aa7`), (c) by the release before #893 (`b90c9591^`), (d)
(e) (f) by goose over the frozen files. All rows ran against the final branch
heads. Each got a seeded project and chat
before the upgrade (the very old one with the pre-enum `state = 'idle'`). Each
row then ran `db migrate status` (with a full `pg_dump` before and after) and
**4 concurrent `db migrate up`** processes. "Schema" is normalized
`pg_dump --schema-only` without either tool's bookkeeping, compared with (a).

| Row | goose before | `status` verdict (exit) | 4 × `up` | After |
|---|---|---|---|---|
| (a) fresh | none | `fresh database: apply 20261014050404_baseline…` (0) | all exit 0; schema current | the reference schema, 3350 lines; identical to the database the last goose release built; no `goose_db_version` |
| (b) goose head | 156 rows, max `20261014055046` | `adopt: goose_db_version holds exactly the 155 versions…` (0) | all 0; **1 ADOPTED**, 0 catch-up | schema identical; `goose_db_version` still 156 rows; rows kept |
| (c) head-1 | 155 rows, max `20261013215013` | `goose catch-up 1, then adopt … (20261014055046)` (0) | all 0; 1 catch-up, 1 adopt | identical; 156 rows; rows kept |
| (d) mid-history | 77/155, through `20260811000000` | `goose catch-up 78, then adopt` (0) | all 0; 1 catch-up, 1 adopt | identical; 156 rows; rows kept; 0 invalid indexes |
| (e) very old | 3/155, through `20260212100000` | `goose catch-up 152, then adopt` (0) | all 0; 1 catch-up, 1 adopt; waiters logged "Waiting for another process's goose catch-up" | identical; 156 rows; `'idle'` carried to `state = 2`; 0 invalid indexes |
| (f) unknown version `20990101000000`, at head and at 77/155 | +1 row | `refuse: … HAS applied 1 goose version(s) the frozen history does not hold: 20990101000000` (1) | all **exit 1**, `NOTHING WAS APPLIED` | full `pg_dump` identical before and after: no DDL, no `schema_migrations` |

`status` was read-only on every row: full `pg_dump` (schema and data,
bookkeeping included) byte-identical before and after.

| Other check | Result |
|---|---|
| Rollback: the last goose release's `db migrate up` on adopted (b), caught-up (c) and (e) | `goose: no migrations to run. current version: 20261014055046`, `schema is current`; its status `applied 155 of 155`; full `pg_dump` unchanged |
| Session-mode PgBouncer, pool capped at 2; `DATABASE_URL` unreachable; `DATABASE_DIRECT_URL` → PgBouncer with no password + `PGPASSWORD` (prod's shape); 4 concurrent on fresh, goose head and mid-history | all exit 0; 1 adopt (head); 1 catch-up + 1 adopt (mid); schema identical; 18 s for all three |
| Dirty schema | status exit 1 `refuse: the schema is dirty … reliant db migrate force 20261014050404`; `up` refuses; `force` clears; `up` current |
| `forge db squash --from-dsn` re-run (pinned) | baseline byte-identical |
| `forge lint --migration-safety` (pinned) | clean |
| Go tests, `REQUIRE_TEST_DB=1` | `internal/db/...` (230 s), `cmd/reliant/commands`, `accountpurge`, `agentruns`: the matrix again in-process (`TestMigrate_*`: fresh, head, head-1 / mid / very old caught up with schema equal to fresh and rows kept, unknown and rolled-back versions refused unchanged, no history refused, concurrent fresh / head / behind-across-CONCURRENTLY with no INVALID index), `TestPlanMigration_Verdicts`, `TestBaselineAdoptsExactlyTheGooseHistory`, `TestGooseHistoryIsFrozen`, `TestForgeMigrationsDeclareCompatibility`, the goose-history tests on a goose-built template |
| `server worker` started before `server api` (throwaway NATS + Temporal), goose mid-history database (77/155) | worker logs "Waiting for database migrations"; api catches up 78, ADOPTS, `/ready` 200; worker turns healthy |
| Same on a goose-head database, then a second boot of it | first: api ADOPTS, worker starts; second: `no pending migrations (version 20261014050404)` |
| `go vet ./...`; pinned `forge generate`; control-plane `go build ./...` through an uncommitted go.work | vet clean; generate changes nothing under `db/` or `internal/db/` (only main's pre-existing dashboard/descriptor drift); control-plane builds except `internal/alerting` (`RuleSource` lacks `Owners` against forge `8d812662`, known and unrelated) |

## 9. goose removal (done 2026-10-22)

Owner decision 2026-10-22: remove goose, no backwards compatibility. The
conditions this section used to list, and how each stood:

1. **Every database we keep has adopted.** Prod: control-plane #1013
   (2026-10-19) records release `20261019.065845-812e040da1af` live in prod.
   That release tree pins reliant `15495975` (2026-10-18), which contains
   #912 and #914, so its api-server booted through `db.Migrate` (bridge, then
   `migratekit.Apply`): a goose database is adopted on that boot or the
   api-server does not start. Later prod releases carry forge migrations past
   the baseline (release `20261021.105910-4bc2c382a106` pins reliant `5454ad45`,
   17 of them), which the migrator applies only on an adopted database.
   control-plane's go.mod has pinned a reliant containing #914 since #930
   (2026-10-15), and `scripts/build-reliant-image.sh` builds the reliant image
   from that pin. dev, dev-k8s and e2e are destroyable (user decision
   2026-10-12). Desktop has no database.
2. **Self-hosted support window.** Waived by the owner: nobody runs the old
   path. reliant v1.8.2 (2026-10-18) is the first tagged release carrying the
   bridge, and the refusal names it as the upgrade step.
3. **Rollback to a goose release is no longer wanted.** Every prod release
   since 2026-10-19 is a forge release, so a rollback lands on one.
4. **What went:** `internal/db/goose_bridge.go`, `RunMigrations`, goose's 156
   frozen files and their embed, the goose-history and goose-data-migration
   tests, `internal/db/migrationcheck` + `cmd/check-migrations` +
   `scripts/check-migrations.sh` (rules over goose's files), the goose-era
   renumbering scripts and hooks, the CI goose steps, and the
   `github.com/pressly/goose/v3` module. `db.Migrate` is `migratekit.Apply`,
   after one refusal of its own: reliant's tables with no forge record
   (`MigrationPlan.refusal`, naming v1.8.2). The baseline's
   `-- forge:adopts goose` lines went too; migratekit records a migration by
   its filename descriptor, not its content, so editing that comment block
   changes nothing on an adopted database
   (`TestMigrate_AdoptedDatabaseIsANoOp`).
5. **Not done:** dropping `goose_db_version` from the databases that still
   hold it. Nothing reads it; a forge migration can drop it whenever.

## 11. Decisions and open items for the user

- `reliant_cp_session` stays at 2 server connections per PgBouncer replica.
  A migration now holds one session there for its run (under a second for an
  adoption), beside the sweep lock; 4 concurrent runs passed behind a cap of 2.
- Resolved by forge `f3fd8c02`: `-- forge:breaks-rollback` replaced
  reliant's own `-- reliant:breaks-rollback`, and `forge lint
  --migration-safety` replaced `TestForgeMigrationsDeclareCompatibility`
  (forge #777); migratekit's run lock no longer deadlocks a
  `CREATE INDEX CONCURRENTLY` (forge #776, §3).

## Not exercised

Cloud SQL itself, the real PgBouncer deployment (a local session-mode
PgBouncer stood in), a k8s rollout, and the dev stack's 5434 database.
