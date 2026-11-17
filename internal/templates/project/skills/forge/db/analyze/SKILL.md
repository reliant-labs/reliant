---
name: analyze
description: Measure what a migration does on a real postgres with forge db analyze — the locks it takes, the tables it rewrites or scans, how long it runs — on a disposable copy of the target; scratch vs dsn (anonymized snapshot) targets, preparing a snapshot (the forge.analyze_target marker, CREATEDB, no connections during the copy), database.analyze config, the five db-analyze-* rules, the forge.db-analyze/v1 JSON document and exit codes.
---

# Measuring migrations: `forge db analyze`

`forge lint --migration-safety` reads migration SQL as text. It cannot know
whether `ALTER COLUMN … TYPE` rewrites the table, which lock a statement takes,
or whether a constraint validation scans every row: that depends on the column
types, the existing schema and the postgres version. `forge db analyze` runs the
migrations and measures them. Keep both: the lint is fast and needs no
postgres; this is the empirical complement.

```bash
forge db analyze            # scratch target, migrations changed since the merge-base
forge db analyze --json     # the forge.db-analyze/v1 document
forge db analyze --base HEAD~3
FORGE_ANALYZE_DATABASE_URL=postgres://… forge db analyze --target dsn
```

It needs postgres, so it is never in a fast PR tier: run it on demand, in a
review lane, or in a full gate.

## What it does

The migrations run on a **disposable database**, never on the target: a dsn
target is read, then copied (`CREATE DATABASE forge_analyze_<random> TEMPLATE
<target>`) and the copy dropped when the run ends — on an error or an interrupt
too; a scratch target is a throwaway pgtest database already. Each pending
migration is applied **as forge's migrator applies it**: the file in one
transaction, `BEGIN`, its statements **one by one**, each measured, `COMMIT`.

Per executed statement:

| Measured | How |
|---|---|
| locks | `pg_locks` of the session, before vs after |
| rewrites | `pg_class.relfilenode` of every user relation, before vs after |
| scans | `pg_stat_xact_user_tables` (`seq_scan`, `seq_tup_read`) delta |
| duration | wall time (meaningful on a target with data) |
| size | `reltuples` on a target with statistics, else `table_rows`, else unknown |

A relation created earlier in the run is **new** and exempt from every rule:
locking or rewriting a table nobody reads yet is free.

A file whose one statement cannot run in a transaction (`CREATE INDEX
CONCURRENTLY`, `DROP INDEX CONCURRENTLY`, `VACUUM` …) runs it **on its own**, as
the migrator does: `executed: true`, its duration, and its lock reported
statically (`SHARE UPDATE EXCLUSIVE`, which blocks neither reads nor writes). So
the safe patterns analyze clean: an index built concurrently and then attached
with `ADD CONSTRAINT … USING INDEX`, a name dropped concurrently and reused.
Only the genuinely non-blocking ones are exempt from the rules (the
`CONCURRENTLY` forms, plain `VACUUM`). `VACUUM FULL`, `CLUSTER` and a
non-concurrent `REINDEX SCHEMA/DATABASE` are judged like any statement: what they
rewrote is measured, and their lock is the one postgres documents (ACCESS
EXCLUSIVE for `VACUUM FULL`/`CLUSTER`; SHARE on the table and ACCESS EXCLUSIVE on
each index for `REINDEX`), so they raise `table-rewrite` (and
`access-exclusive-lock`) with a fix for the operation.

A lock one statement takes is held through every later statement **of the same
file** — the rules count it (`held_before: true` on the later statement). Each
file commits before the next, so no lock carries over between files.

Refused as unsupported (exit 2): a file that controls its own transaction
(`BEGIN`/`COMMIT`/`SAVEPOINT` — split the file instead), and a file that puts a
non-transactional statement beside others (postgres refuses it as written). A
statement that changes what the whole **server** shares — a role, a database, a
tablespace, `ALTER SYSTEM`, a subscription — is not run (`executed: false`):
a copy of one database does not isolate it.

## Targets

| `--target` | Runs on | Analyzes |
|---|---|---|
| `scratch` (default) | pgtest's embedded postgres (or `FORGE_TEST_POSTGRES_URL`), at `database.postgres_version` | migrations added or changed since `--base` (default: merge-base with the remote default branch); the unchanged ones are applied first as setup, from a cached template |
| `dsn` | a database you provide — an **anonymized snapshot of prod** | every migration its `schema_migrations` lacks |

On a scratch target tables are empty: rewrites, locks and validation scans are
still observed exactly (they do not depend on rows), but durations are not
meaningful and sizes come only from `table_rows` hints. On a snapshot with data a
rewrite or a scan really runs on the copy (bounded by `statement_timeout`) —
that cost is the measurement.

### Preparing a snapshot for a dsn target

Whoever builds the snapshot does three things once:

1. **Mark it** as its owner: `ALTER DATABASE <db> SET forge.analyze_target = 'on';`
   An unmarked target is refused (exit 2) with that line. The marker is the
   database's own setting (a role's or a session's does not count). Never mark
   production.
2. **Let the analysis role copy it**: the role needs `CREATEDB` and must own the
   database (or be a superuser, or the database is `IS_TEMPLATE`).
3. **Keep it to itself during a run**: postgres copies a database only while no
   other session is connected to it. A run that finds one connected fails (exit
   2) and names who (`pg_stat_activity`). The copy takes about as long as
   copying the files; the document reports it as `target.clone`
   (`database`, `duration_ms`, `size_bytes`).

A dsn target is also **refused** when it is the database a non-development env
declares (`deploy/kcl/<env>/`), judged by where its DSN actually connects
(host/port/dbname query parameters, keyword/value DSNs; a multi-host DSN is
refused). A target ahead of the tree, or dirty, cannot be analyzed (exit 2).

## Configuration

```yaml
# forge.yaml
database:
  analyze:
    target:
      kind: dsn                          # scratch (default) | dsn: a marked snapshot, analyzed on a copy (above)
      dsn_env: FORGE_ANALYZE_DATABASE_URL  # the env var's NAME; never a DSN in forge.yaml
      statement_timeout: 60s             # per statement (default 60s); a timeout is a finding
      lock_timeout: 5s                   # default 5s; another session's lock stops the run (exit 2)
    table_rows:                          # size hints when the target cannot size a table
      orders: 40000000
    large_table_rows: 1000000
    small_table_rows: 10000              # findings on a table known to be smaller are dropped
```

Flags override per run: `--target`, `--dsn-env`, `--base`, `--dir`, `--json`.

## Rules

Each warns first through forge's lint rollout and is dialled through
`lint.rules` like any forge rule (`{db-analyze-table-rewrite: off}`).

| Rule | Fires when | Fix |
|---|---|---|
| `db-analyze-table-rewrite` | a pre-existing table or its indexes were rewritten (`int`→`bigint`, a volatile `DEFAULT random()`) | add the column with no or a constant default; widen only binary-compatibly (`varchar(10)`→`varchar(20)`/`text`); otherwise expand/contract: new column, batched backfill, switch |
| `db-analyze-access-exclusive-lock` | ACCESS EXCLUSIVE (blocks reads too) held across a scan or rewrite of a pre-existing table | keep the long part out from under it (below) |
| `db-analyze-validation-scan` | a full scan under a lock that blocks writes: `ADD CONSTRAINT` without `NOT VALID`, `SET NOT NULL` with no valid `CHECK`, a foreign key (scans both tables) | `ADD CONSTRAINT … NOT VALID`, then `VALIDATE CONSTRAINT` **in a later migration file**; for NOT NULL: `CHECK (c IS NOT NULL) NOT VALID`, validate, then `SET NOT NULL` (postgres skips the scan) |
| `db-analyze-blocking-index-build` | an index built on a pre-existing table without `CONCURRENTLY` (SHARE lock blocks writes) | `CREATE INDEX CONCURRENTLY` alone in its file; for a UNIQUE/PK constraint, build the index concurrently then `ADD CONSTRAINT … USING INDEX` |
| `db-analyze-statement-timeout` | a statement exceeded `statement_timeout` on the target | batch it, or split it so the long part takes no blocking lock |

Measured on postgres 18 (and pinned by forge's tests): `varchar` widening, a
constant default, `DEFAULT now()`, `ADD COLUMN … NOT NULL DEFAULT 0` and any
`NOT VALID` constraint neither rewrite nor scan; a type change of an indexed
column rebuilds the index as part of the table's rewrite (`table-rewrite`, not
`blocking-index-build`); `VALIDATE CONSTRAINT` scans
under SHARE UPDATE EXCLUSIVE, which blocks no writes — unless the same file's
`NOT VALID` statement already holds ACCESS EXCLUSIVE. `TRUNCATE` is left to the
textual lane's `destructive-change`.

## The document (`--json`)

`schema: forge.db-analyze/v1`. `target` (`kind`, `identity` host:port/db — never
credentials — `server_version`, `has_statistics`, and for a dsn target `clone`),
`base`, `migrations[]` (each
`statements[]` with `line`, `sql`, `executed`, `duration_ms`, `locks[]`,
`rewrites[]`, `scans[]`, `rows{}`), `queries[]` (the query-plan pass; empty
here), `findings[]` (`rule`, `severity`, `file`, `line`, `relation`, `message`,
`fix`, `rows`, `note` for a warn-first finding), `summary`
(`migrations_scanned`, `statements_executed`, `reason` when nothing was scanned),
`exit_code`, `error`.

Exit codes: **0** nothing gating; **1** a gating finding; **2** could not
analyze (no postgres, target ahead of the tree or dirty, refused or unmarked
DSN, a copy that could not be made, a file that controls its own transaction, a
statement that failed, another session's lock). An empty analysis is exit 0
with `migrations_scanned: 0` and a `reason` — read the reason, it is never a
silent pass.

A reviewer (`pr-review`) reads the document: deterministic findings are
reported as such; judgement about a specific table ("this one is hot") is a
comment, not a gate.

The mechanism is a library, `forge/pkg/dbanalyze` (`forge project libraries
dbanalyze`): the read-only source and the disposable clone, a session applying
files as the migrator does, the measured executor, the rules and the document
types.
