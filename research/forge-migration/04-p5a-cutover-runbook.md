# 04 — P5a cutover runbook: goose → forge migratekit

Source: the F1 implementation (forge #648 run lock, #649 `-- forge:adopts`), which was rehearsed against reliant's real goose history in a throwaway postgres. **Never run any of this against port 5434.**

Rehearsal result (2026-10-12):
- goose v3.28 built the schema from **151** files.
- The cut-over binary's `db migrate up` printed `ADOPTED … verified=151`, applied the next migration, and was a no-op on re-run.
- A fresh database applied the baseline plus the next migration.
- `pg_dump --schema-only`: the adopted database and the baseline-built database are identical (3282 lines each).
- A database missing the newest file was refused with `missing=[20261011203444]`, then adopted once caught up.

## 0. Prerequisites
- forge #648 and #649 released and pinned in reliant (rollout rule 7).
- The cutover PR does nothing else (rule 5) and freezes goose: no goose-migration PRs in flight when it lands.

## 1. Generate the version list (never hand-typed)
```bash
ls internal/db/migrations/postgres/*.sql | xargs -n1 basename | cut -d_ -f1 \
  | sort -n | xargs -n8 | sed 's/^/-- forge:adopts goose /' > /tmp/adopts.sql
grep -o '[0-9]\{14\}' /tmp/adopts.sql | wc -l; ls internal/db/migrations/postgres/*.sql | wc -l   # must match
```

## 2. Cut the baseline in a throwaway container (named, removed afterwards; image = prod's PG major)
```bash
docker run -d --name reliant-baseline-cut -e POSTGRES_PASSWORD=cut -p 127.0.0.1::5432 postgres:16
PORT=$(docker port reliant-baseline-cut 5432/tcp | cut -d: -f2)
docker exec reliant-baseline-cut psql -U postgres -c 'CREATE DATABASE goose_built'
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir internal/db/migrations/postgres \
  postgres "postgres://postgres:cut@127.0.0.1:$PORT/goose_built?sslmode=disable" up -allow-missing
reliant forge db migration new baseline          # db/migrations/<ts>_baseline.up.sql
B=$(ls db/migrations/*_baseline.up.sql)
{ cat /tmp/adopts.sql; echo; docker exec reliant-baseline-cut pg_dump -U postgres --schema-only \
    --no-owner --no-privileges -T goose_db_version -T goose_db_version_id_seq goose_built \
  | grep -v -e '^SET ' -e '^SELECT pg_catalog.set_config' -e '^\\'; } > "$B"
```
**Strip pg_dump's session prelude (as above).** Otherwise:
- `set_config('search_path','',false)` sticks to the migrator's connection, so the next migration fails with `relation "chats" does not exist`, and `AutoMigrate` returns pool connections with an empty `search_path`.
- pg_dump 16.15's `\restrict` line is a psql meta-command that fails as SQL.

## 3. Wire reliant to migratekit
- `MigrateApply` / `reliant db migrate up` → `migratekit.AutoMigrate` / `Open` + `Up`.
- `WaitForSchema` polls `schema_migrations` (clean, at or past the newest embedded version) instead of `goose_db_version`.
- Delete the goose files, embed and dependency. **Keep the `goose_db_version` table** (rollback target).
- Add a guard test that `internal/db/migrations/postgres/` no longer exists.
- **Migrate on `DirectURL` when it is set.** The run lock and the applied-record lock are session advisory locks, which break behind a transaction-pooling PgBouncer. Today's goose path deliberately uses `URL` only because goose takes no lock.
- sqlc stays until P5b, as a `forge.yaml` `generate.hooks` entry (forge #641).

## 4. Prove it before any deploy (attach the output to the PR)
In the throwaway container, with the cutover binary:
- `goose_built` → `db migrate up` prints `ADOPTED … verified=<N>`.
- A fresh database → the baseline applies.
- `pg_dump --schema-only` (excluding `goose_db_version`, its sequence and the three `schema_migrations*` tables) diffs empty.
- A database built without the last goose file is refused.
- After `AutoMigrate`, `SHOW search_path` is not empty.

Then `docker rm -fv reliant-baseline-cut`.

## 5. Read-only pre-flight per environment

**Dev databases are destroyed and rebuilt from the baseline instead (user decision, 2026-10-12)**, so this pre-flight and the adoption path apply to prod (and any env whose data we keep). Never run a dev teardown against prod.

```sql
SELECT version_id FROM (SELECT DISTINCT ON (version_id) version_id, is_applied
  FROM goose_db_version ORDER BY version_id, id DESC) t
WHERE is_applied AND version_id <> 0 ORDER BY 1;
```
`comm -3` this against `grep -o '[0-9]\{14\}' /tmp/adopts.sql | sort`; the result must be empty. The shared dev database (5434) will likely show branch-only goose versions. Decide each one (reconcile the schema and delete that goose row, or reset that database) **with the user's sign-off**, before its turn.

## 6. Rollout: dev → staging → preprod → prod
For each environment:
1. Take a backup.
2. For preprod and prod, first run the new binary's `db migrate up` against a Cloud SQL clone.
3. Deploy.
4. Confirm the `ADOPTED` line, `schema_migrations` at the baseline version (or later) with `dirty = false`, and `goose_db_version` unchanged.

Rollback = redeploy the last goose release; goose sees every version applied and does nothing. Once forge migrations land on top of the baseline, the usual backward-compatible rules apply.

## 7. Desktop
The first launch adopts the database if the user ran the last goose release; otherwise it refuses with "run the previous release first". This is acceptable pre-launch. If skipped releases ever matter, ship one bridge release that runs `goose.Up` on the frozen files before `AutoMigrate`.

## Not yet exercised
- A real k8s init container (it runs `Migrator.Up`, which is tested), Electron, Cloud SQL itself, and PgBouncer.
