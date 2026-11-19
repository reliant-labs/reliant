# `forge db analyze`: empirical analysis of migrations and queries

Status: accepted (owner, 2026-11). Phase 1 in progress.

## Problem

`forge lint --migration-safety` (`internal/linter/migrationlint`) reads migration SQL as
text. It cannot tell whether an `ALTER COLUMN … TYPE` rewrites the table, which lock a
statement takes, or whether a constraint validation scans the whole table: those depend on
the column types, the Postgres version and the existing schema. Nothing at all looks at the
queries an app runs: a new filter or join with no index merges unnoticed, and the generated
list search over text columns is the likeliest sequential scan in every project.

## Decision

forge owns the mechanism and a stable JSON document; reviewers (the Reliant `pr-review`
workflow, or any user's) consume it.

- **Deterministic findings gate**, and every new rule warns first through the lint rollout
  table (`internal/cli/lint/lint_rollout_table.go`).
- **LLM judgement is advisory**: the reviewer reads the JSON (plans, locks, sizes, file:line)
  and comments; it never blocks on its own verdict.
- **The database it runs against is configurable.** The default is a scratch Postgres
  (`forge/pkg/pgtest`), which needs nothing. A project can point it at a database with real
  data and real statistics, typically an anonymized snapshot of prod, and get answers that
  depend on size (durations, real plans).
- It needs Postgres, so it is **never in a fast PR tier** (`task ci`). It runs in the review
  lane, `ci:full`, or on demand.

## Configuration

```yaml
# forge.yaml
database:
  analyze:
    target:
      # scratch (default): pgtest's embedded Postgres (or FORGE_TEST_POSTGRES_URL),
      #   migrated to the base, then the change applied.
      # dsn: a database you provide, e.g. an anonymized prod snapshot, marked with
      #   ALTER DATABASE <db> SET forge.analyze_target = 'on'; it is read and copied,
      #   and the migrations run on the copy (see "Safety of a dsn target").
      kind: dsn
      # The NAME of the env var holding the DSN. A DSN is never written in forge.yaml.
      dsn_env: FORGE_ANALYZE_DATABASE_URL
      statement_timeout: 60s # per statement; a timeout is itself a finding
      lock_timeout: 5s
    # Size hints for a scratch target (it has no rows). Ignored when the target has
    # statistics of its own (a dsn target that has been ANALYZEd).
    table_rows:
      orders: 40000000
    large_table_rows: 1000000 # at or above this a table is "large"
    small_table_rows: 10000 # below this, scan/index findings are dropped
```

Flags override per run: `--target scratch|dsn`, `--dsn-env NAME`, `--base REV`, `--json`.
Rule severities go through `lint.rules` like every other rule (`{db-analyze-…: off}`).

### Safety of a `dsn` target

The target is read, copied and never written. An earlier draft ran everything in one
transaction that was always rolled back and rejected a copy as unnecessary; review showed
that model does not hold. A migration's own `COMMIT` ends the transaction and commits
whatever came before it, a sequence advances whether or not its transaction commits, and a
statement that cannot run in a transaction cannot be measured inside one, so a later
migration that depends on it (`ADD CONSTRAINT … USING INDEX`) fails. A rolled-back
transaction also holds every lock a file took into the next file, which forge's migrator
never does, and hides the lock a later file needs again. So:

- **The target must be marked** by whoever builds it, once, as its owner:
  `ALTER DATABASE <db> SET forge.analyze_target = 'on'`. The marker is read from
  `pg_db_role_setting` for the database itself (a role's or a session's setting does not
  count). An unmarked target is refused (exit 2) with that line. Production carries no
  marker, so a DSN that reaches it, however it is spelled and whether or not forge can
  resolve what a deploy env declares, is refused.
- As a second check, the DSN is refused when it reconciles (`databaseIdentity`,
  `internal/cli/db_target.go`) with a database a non-development deploy env declares. The
  comparison reads the DSN the way pgx dials it (host, port and dbname query parameters,
  keyword/value DSNs, multi-host lists). An env whose DSN cannot be resolved is not
  compared: the marker is the guard there.
- **The analysis runs on a copy.** forge reads the target's marker, version, statistics and
  `schema_migrations` over a read-only connection, closes it, and copies the target with
  `CREATE DATABASE forge_analyze_<random> TEMPLATE <target> STRATEGY FILE_COPY` (plain
  `TEMPLATE` before Postgres 15) from a maintenance connection to the same server. The copy
  is dropped when the analysis ends, on an error and on an interrupt too. The document
  reports its name, size and copy time as `target.clone`.
- The copy needs a role with `CREATEDB` that owns the target (or a superuser, or a target
  marked `IS_TEMPLATE`), and no other session connected to the target while it is copied.
  Either failing is exit 2 with the cause; for connected sessions, who they are
  (`pg_stat_activity`).
- **Migrations are applied as forge's migrator applies them**: each file `BEGIN`, its
  statements one at a time (each measured), `COMMIT`. Locks never carry over between files.
  A file whose one statement cannot run in a transaction (`CREATE INDEX CONCURRENTLY`,
  `DROP INDEX CONCURRENTLY`, `VACUUM`) runs it on its own, on the copy, with its lock reported
  statically (`executed: true`). Only the non-blocking ones (the `CONCURRENTLY` forms, plain
  `VACUUM`) are exempt from the rules: `VACUUM FULL`, `CLUSTER` and a non-concurrent
  `REINDEX SCHEMA/DATABASE` are judged with their measured rewrites and documented locks.
  A file that controls its own transaction (`BEGIN`/`COMMIT`) is refused as unsupported, as is one that puts a non-transactional statement beside
  others (Postgres refuses it in the implicit transaction the migrator runs a file in).
- A statement that changes what the whole server shares (roles, databases, tablespaces,
  `ALTER SYSTEM`, subscriptions) is not run: a copy of the database does not isolate it.
  A `DO` block or function can make such changes out of sight; that is not detected.
- On a snapshot with data, a rewrite or a scan really runs (bounded by `statement_timeout`)
  on the copy. That cost is the point: it measures it.

A scratch target is disposable already (a pgtest database cloned from a cached template),
so migrations run on it directly, in the same per-file model, with no second copy.

## What is analyzed

`--base REV` (default: the merge-base with the remote default branch) splits the migration
directory:

- **scratch**: migrations unchanged since base are applied first as setup (a pgtest
  template keyed by their content, so the setup is reused); migrations added or changed
  since base are applied one statement at a time and analyzed.
- **dsn**: every migration the target's `schema_migrations` lacks is analyzed (it will run
  against prod-shaped data). A target ahead of the tree (versions the tree does not have) is
  an error (exit 2).

An empty set is a verdict, not a silent pass: the document says scanned 0, and why.

## Phase 1: migrations

Per executed statement, inside its file's transaction:

| Measured            | How                                                                                              |
| ------------------- | ------------------------------------------------------------------------------------------------ |
| locks taken         | diff of `pg_locks WHERE pid = pg_backend_pid()` before/after, mapped to relation names and modes |
| table/index rewrite | `pg_class.relfilenode` of every user relation before/after                                       |
| scans               | `pg_stat_xact_user_tables` (`seq_scan`, `seq_tup_read`) delta                                    |
| duration            | wall time (meaningful on a dsn target with data)                                                 |
| size                | `reltuples` on a target with statistics, else `table_rows`, else unknown                         |

A relation created earlier in the same migration run is "new" and exempt: locking or
rewriting an empty table nobody reads yet is free.

Rules (each warn-first via the rollout table; IDs prefixed `db-analyze-`):

- `db-analyze-table-rewrite`: a pre-existing table or its indexes were rewritten.
- `db-analyze-access-exclusive-lock`: a pre-existing table held `ACCESS EXCLUSIVE` (blocks
  reads and writes) across a statement that also scanned or rewrote it.
- `db-analyze-validation-scan`: a full scan of a pre-existing table under a lock that blocks
  writes (`ADD CONSTRAINT` without `NOT VALID`, `SET NOT NULL` without a `CHECK`).
- `db-analyze-blocking-index-build`: `CREATE INDEX` without `CONCURRENTLY` on a
  pre-existing table (`SHARE` lock: blocks writes for the whole build).
- `db-analyze-statement-timeout`: a statement exceeded `statement_timeout` on the target.

Findings below `small_table_rows` (when the size is known) are dropped. The textual
`--migration-safety` lane stays: it is fast and needs no Postgres; this lane is the
empirical complement.

## Phase 2: query capture

`forge/pkg/orm` installs one more bun query hook **only when `FORGE_QUERY_CAPTURE_DIR` is
set** (zero cost otherwise). Running the tests with it set
(`FORGE_QUERY_CAPTURE_DIR=… task test:short`) records each distinct query shape: the SQL
with `$n` placeholders (forge binds values, `pkg/crud/bind.go`), normalized (whitespace,
`IN` lists), its fingerprint, and its caller: the first stack frame outside bun,
`database/sql` and forge's own `pkg/orm` and `pkg/crud`. One JSONL file per process.

Queries no test runs are invisible. That is the intended incentive.

## Phase 3: query plans

`forge db analyze queries --capture DIR` selects the shapes whose caller is in a file
changed since base (`--all` for every one), applies the head schema on the migration pass's
copy of the target (a scratch database, or the dsn target's `forge_analyze_*` clone, which
stays open until the query pass is done), and explains each with `EXPLAIN (GENERIC_PLAN, FORMAT JSON)`
(Postgres 16+; older is exit 2) twice: as is, and with `SET LOCAL enable_seqscan = off`.

- `db-analyze-query-no-usable-index`: under `enable_seqscan = off` a table under a filter,
  join or `ORDER BY … LIMIT` is not served by an index. Served means its scan node has an
  `Index Cond` (or an index order satisfying the `ORDER BY`). Unserved means a scan marked
  `Disabled: true` (PG 18), or an index scan carrying only a `Filter`: the planner may walk
  an unrelated index in full rather than seq-scan, so an index node alone proves nothing.
  True at any size, so it works on an empty scratch database. Severity comes from the
  table's size (statistics on a dsn target, else `table_rows`, else warning).
- `db-analyze-query-large-seqscan` (dsn target with real statistics only): the normal plan
  seq-scans a table at or above `large_table_rows`.

A scratch target does not borrow row counts. Restoring statistics
(`pg_restore_relation_stats`, or writing `pg_class`) on an empty table makes plans worse:
the planner keeps the restored rows-per-page ratio but multiplies it by the real block
count, zero, so every table plans as one row (`db-analyze-stats-spike.md`, measured on
18.3). Real plans at size need a dsn target with data.

## Phase 4: review integration (control-plane)

`pr-review` runs `forge db analyze --json` when a diff touches `db/migrations/`, the ORM
package or `*_repo_ext.go`, and gives the document to the reviewer model with the diff.
Deterministic findings are reported as such; the model's judgement ("this table is hot,
this lock is unacceptable") is a comment.

## Later: production feedback

`auto_explain` and `pg_stat_statements` on hosted Postgres, surfaced through `forge obs`,
show the plan regressions pre-merge analysis cannot.

## Output document (`--json`)

```json
{
  "schema": "forge.db-analyze/v1",
  "target": {
    "kind": "scratch",
    "identity": "localhost:5432/forge_analyze_x",
    "server_version": "18.1",
    "has_statistics": false
  },
  "base": "origin/main@abc123",
  "migrations": [
    {
      "file": "db/migrations/20261101_add_status.up.sql",
      "version": 20261101,
      "analyzed": true,
      "statements": [
        {
          "index": 0,
          "line": 3,
          "sql": "ALTER TABLE orders ALTER COLUMN status TYPE text",
          "executed": true,
          "duration_ms": 12,
          "locks": [{ "relation": "orders", "mode": "AccessExclusiveLock" }],
          "rewrites": [{ "relation": "orders", "kind": "table" }],
          "scans": [{ "relation": "orders", "seq_scan": 1, "seq_tup_read": 0 }],
          "rows": { "orders": { "value": 40000000, "source": "table_rows" } }
        }
      ]
    }
  ],
  "queries": [],
  "findings": [
    {
      "rule": "db-analyze-table-rewrite",
      "severity": "warning",
      "file": "…",
      "line": 3,
      "relation": "orders",
      "message": "…",
      "fix": "…"
    }
  ],
  "summary": {
    "migrations_scanned": 1,
    "statements_executed": 1,
    "queries_scanned": 0,
    "skipped": []
  },
  "exit_code": 0
}
```

A dsn target's `target` also carries `"clone": {"database": "forge_analyze_…",
"duration_ms": …, "size_bytes": …}`, the copy the migrations ran on.

Exit codes: **0** nothing gating; **1** a gating finding; **2** could not analyze (no
Postgres, Postgres too old, target ahead of the tree, refused or unmarked DSN, a copy that
could not be made, a file that controls its own transaction). Never "passed" when nothing
was examined.
