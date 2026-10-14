# 04 — P5a cutover runbook: goose → forge migratekit

**Never run any of this against port 5434, prod, Cloud SQL or a gke context
from an agent session.** Steps marked **(human)** touch a deployed database and
are run by a person with the user's sign-off.

Revised 2026-10-14 for the user's condition on approving P5a: *"Let's not break
prior users or the prod db."* Against forge `8d812662` (reliant #885), on
throwaway Postgres 16. The implementation is two reliant branches, merged back
to back (§6.0):

| Branch | What | Changes a deployed DB? |
|---|---|---|
| `feat/p5a-forge-migrations-baseline-r2` | `db/migrations/20261014050404_baseline.up.sql` (goose's 156 migrations, squashed by `forge db squash --from-dsn`, with its `-- forge:adopts goose` list); `db/embed_gen.go`; tests that freeze goose's history by version AND content | No — nothing runs forge's migrator yet |
| `feat/p5a-migrate-up-migratekit-r2` | `db.Migrate`: the goose bridge, then `migratekit.Apply`; `db migrate status` as a read-only preview; `force`; the backward-compatible rule; sqlc reads `db/migrations` | **Yes — this is the cutover** |

`chore/p5a-remove-goose` is **not** part of the cutover; §9 says what must be
true before goose can go.

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
   deliberately `-- reliant:breaks-rollback <why>`), enforced by
   `TestForgeMigrationsDeclareCompatibility`; `make migration` writes the
   default. That keeps "redeploy the last goose release" a working rollback.
4. The baseline was re-cut for `20261013215013_worktree_failure_reason` (#861),
   `20261014055046_integration_writes` (#893) and
   `20261014142537_trigger_event_origin` (#909): **156** goose migrations,
   head `20261014142537`, cut from a database main `111d67a5` built. It is now `forge db squash
   --from-dsn` output end to end (header, adoption list and body), and
   re-running the squash writes the same bytes.
5. `db migrate up` prints what happened from `migratekit.Apply`'s `Result` and
   the bridge's report; the stale "a Migrator holds a second session" notes are
   gone (forge #706 made every migratekit path one session).

## 1. Cut the baseline (done; how to re-cut)

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

Then update `frozenGooseDigest` in `internal/db/baseline_adoption_test.go`
(the test prints the new digest). A goose migration that must ship before the
cutover means re-cutting from the new goose head; after the cutover, goose is
frozen and a schema change is a forge migration.

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

- `make migration NAME=x` runs the pinned `forge db migration new` and starts
  the file with `-- forge:backward-compatible — <why …>`. Replace the
  placeholder; `TestForgeMigrationsDeclareCompatibility` fails while it is
  there, and fails a migration that declares neither
  `-- forge:backward-compatible` nor `-- reliant:breaks-rollback <why>`.
- Why it matters: rolling back is redeploying the previous release onto the
  schema the new one left. Until a release after the cutover ships, that is
  the **last goose release**, whose code must run on every forge migration
  applied since. After that it is an older forge release, whose migrator
  refuses to start on a schema ahead of it unless every newer version was
  declared compatible. Expand in one release, contract in a later one.
- A `-- reliant:breaks-rollback` migration changes §7: from that release on,
  rollback means a forward fix.
- **No `CREATE INDEX CONCURRENTLY` in a forge migration for now.** migratekit's
  run-lock waiters block in `pg_advisory_lock`, which holds a snapshot the
  index build waits for. Reproduced with forge `8d812662`'s `migratekit.Apply`,
  4 concurrent runners over `CREATE TABLE` + a `CREATE INDEX CONCURRENTLY`
  migration, 3 trials: every time the holder finished and the 3 waiters failed
  `take the migration run lock: ERROR: deadlock detected (SQLSTATE 40P01)` —
  i.e. every other api-server replica fails its boot and restarts. Reported to
  forge (§11).

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

## 5. Read-only pre-flight, per database we keep

```bash
DATABASE_URL=<dsn> reliant db migrate status      # the NEW binary
```

The first line is the verdict. For prod it should be `adopt: …`, or
`goose catch-up N, then adopt` if the newest goose migrations (#861, #893, #909)
never deployed there; both are fine.
Anything starting `refuse:` is resolved first, with the user. Exit 1 means
`up` would refuse.

## 6. Rollout

### 6.0 Merge order
1. `docs/p5a-runbook-r2` any time.
2. `feat/p5a-forge-migrations-baseline-r2`, then `feat/p5a-migrate-up-migratekit-r2`
   **back to back** (the second is stacked on the first). The baseline
   freezes goose the moment it merges. A goose migration merged in between
   fails `TestBaselineAdoptsExactlyTheGooseHistory` and needs a re-cut (§1).
3. **The next prod deploy after (2) carries adoption.** Do §6.3 first, and
   tag the last pre-cutover release image (`[keep]`): it is the rollback.
4. A human with workflow permission lands the `check-migrations.yml` change (§10).

### 6.1 Dev databases **(human)**
Destroyable (user decision 2026-10-12), but no longer required to be dropped:
a dev database at any goose version on the frozen history is caught up and
adopted on the next `forge env up dev`. Run `status` first. One holding a
branch-only goose version is refused; drop and recreate that one:

```bash
cd ../control-plane
docker compose exec postgres psql -U postgres -c 'DROP DATABASE "reliant<sfx>" WITH (FORCE)' -c 'CREATE DATABASE "reliant<sfx>"'
forge env up dev        # the reliant-migrate initContainer runs `reliant db migrate up`
```

Check: `reliant-migrate` ends `schema is current (version 20261014050404)`.

### 6.2 dev-k8s / e2e
Same: `status`, then the next deploy adopts (or rebuild if refused).

### 6.3 Prod rehearsal on a Cloud SQL clone **(human, before the merge's first prod deploy)**
```bash
# 1. backup
gcloud sql backups create --instance=reliant-pg --project=reliant-labs-475814 \
  --description="pre-P5a goose->forge cutover [keep]"
# 2. clone
gcloud sql instances clone reliant-pg reliant-pg-p5a-rehearsal --project=reliant-labs-475814
# 3. status, up and a schema diff against the clone (cloud-sql-proxy to the clone; the NEW binary; role `reliant`)
export PGPASSWORD=<pw>; C=postgres://reliant@127.0.0.1:<port>/reliant_cp
DATABASE_URL=$C reliant db migrate status                    # "adopt: …" (or "goose catch-up N, then adopt")
pg_dump --schema-only --no-owner --no-privileges -T goose_db_version -T goose_db_version_id_seq -T 'schema_migrations*' "$C" > before.sql
psql "$C" -tAc 'SELECT count(*) FROM goose_db_version' > goose-before.txt
DATABASE_URL=$C reliant db migrate up                         # ADOPTED … (156 goose versions verified); schema is current (version 20261014050404)
pg_dump --schema-only --no-owner --no-privileges -T goose_db_version -T goose_db_version_id_seq -T 'schema_migrations*' "$C" > after.sql
diff before.sql after.sql                                     # empty for "adopt"; for a catch-up, only what the caught-up goose migrations add
psql "$C" -tAc 'SELECT count(*) FROM goose_db_version'        # = goose-before.txt for "adopt" (+1 for a catch-up)
DATABASE_URL=$C <last goose release> db migrate up            # rollback rehearsal: "goose: no migrations to run", "schema is current"
DATABASE_URL=$C reliant db migrate status                     # "up to date (version 20261014050404)"
# 4. delete the clone
gcloud sql instances delete reliant-pg-p5a-rehearsal --project=reliant-labs-475814
```

### 6.4 Prod **(human)**
5. **status against prod**, with the new image: a one-off pod in
   `control-plane-prod` with the reliant-api-server env (`DATABASE_URL`,
   `DATABASE_DIRECT_URL`, `PGPASSWORD` from
   `pgb.client_env(..., "reliant", "reliant-db-role", "reliant_cp", "reliant_cp_session")`).
   It must match the clone's verdict. Note `SELECT count(*) FROM goose_db_version`.
6. **Deploy.** The first new api-server pod adopts (under a second); the old
   pods keep serving untouched; new workers and gateways wait for it.
7. **Verify:**
   - an api-server log line `ADOPTED a database goose migrated: 20261014050404_baseline.up.sql recorded as applied without running its SQL (156 goose versions verified; …)` at WARN, on exactly one pod;
   - `SELECT version, dirty FROM schema_migrations` → `20261014050404 | f`;
   - `goose_db_version` row count equal to step 5's (N more if step 5 said `goose catch-up N`);
   - `reliant db migrate status` → `up to date (version 20261014050404)`;
   - api `/ready`, workers started, chats work.
8. **Rollback = redeploy the last goose image** (§7).

## 7. Rollback

Redeploy the last goose release image. goose finds every version it knows
applied and does nothing; its waiters read `goose_db_version`, which still says
current. `schema_migrations*` stay, ignored. Rolling forward again is a no-op
(already adopted). This holds as long as every forge migration since the
cutover is `-- forge:backward-compatible` (§3).

## 8. Desktop and self-hosted

- **Desktop (Electron) has no database.** It spawns only
  `reliant daemon start --server <api url>`; `data-migration.js`'s `reliant.db`
  moves are a SQLite-era leftover. There is no monolith mode.
- **Self-hosted compose** (`docker-compose.cloud.yml`) runs `server api`
  against its own Postgres and migrates at boot through `db.Migrate`. Any
  install on the goose history — at head, or any number of releases behind —
  is caught up and adopted by the first new release it runs. The catch-up of
  a very old database runs goose's whole remaining history at boot (152
  migrations took about 5–10 s on an empty test database; data migrations scale
  with the data).

## 9. What must be true before goose can go (`chore/p5a-remove-goose`)

Removing goose deletes the bridge: a database behind goose's head is then
refused (migratekit's `AdoptionError`) and needs the last bridge-carrying
release first. So, all of:

1. Every database we keep has adopted: `status` says `up to date` (prod,
   dev-k8s, e2e, any dev stack someone keeps).
2. Self-hosted users have had a bridge-carrying release for a support window
   the user chooses, and that release's image tag is kept and documented as
   the upgrade step for anything older.
3. Rollback to a goose release is no longer wanted: at least one forge release
   after the cutover has shipped to prod, so the rollback target is a forge
   release.
4. The removal changes the refusal text to name that bridge release, and
   takes with it goose's files, `RunMigrations`, the goose-history tests, the
   `goose` module and the CI goose steps. `chore/p5a-remove-goose` (b3ebfbf7)
   predates the bridge and the re-cut and must be redone, not merged.
5. Optionally later: a forge migration drops `goose_db_version`.

## 10. For a human with workflow permission

`.github/workflows/check-migrations.yml` (the GitHub App token cannot push it):
- `paths:` add `db/migrations/**` and `internal/db/migrate*.go`,
  `internal/db/goose_bridge.go`; drop `internal/db/postgres/schema.sql`.
- Replace "Validate schema.sql agrees with the migrations" (the schema.sql
  drift tests are deleted, so that step now fails) with:
  ```yaml
  - name: Validate migrations apply, adopt, catch up and refuse
    env:
      DATABASE_URL: postgres://postgres:postgres@localhost:5432/reliant_ci?sslmode=disable
      REQUIRE_TEST_DB: "1"
    run: |
      go test ./internal/db/ -count=1 \
        -run 'TestBaselineAdopts|TestGooseHistoryIsFrozen|TestForgeMigrationsDeclareCompatibility|TestMigrate_|TestPlanMigration|TestPendingMigrations|TestWaitForSchema'
  ```
- Keep the goose steps (they still check the frozen files) until §9.

## 11. Decisions and open items for the user

- `reliant_cp_session` stays at 2 server connections per PgBouncer replica.
  A migration now holds one session there for its run (under a second for an
  adoption), beside the sweep lock; 4 concurrent runs passed behind a cap of 2.
- `-- reliant:breaks-rollback` is reliant's own marker. forge has no
  "deliberately not compatible" spelling and no check that a migration
  declares either (reported as forge friction).
- `CREATE INDEX CONCURRENTLY` in a forge migration fails every concurrent
  runner but one until forge's run lock stops blocking with a snapshot (forge
  friction, §3). Reliant's goose bridge already polls for its own lock.

## Not exercised

Cloud SQL itself, the real PgBouncer deployment (a local session-mode
PgBouncer stood in), a k8s rollout, and the dev stack's 5434 database.
