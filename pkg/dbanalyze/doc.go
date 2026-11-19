// Package dbanalyze measures what SQL does on a real postgres — the locks a
// migration statement takes, the tables it rewrites or scans, how long it
// runs — on a DISPOSABLE database, and reports it as the
// `forge.db-analyze/v1` document `forge db analyze` prints.
//
// The pieces, for any pass that analyzes a target:
//
//   - [OpenSource] reads a dsn target (its marker, version, statistics and
//     schema_migrations) without writing to it; [CloneDatabase] copies it
//     into forge_analyze_<random> with CREATE DATABASE … TEMPLATE, and
//     [Clone.Close] drops the copy. The target itself is never written.
//   - [PrepareScratch] is a scratch target: pgtest's embedded postgres,
//     migrated with a base set of migrations from a cached template.
//   - [Open] starts a [Session] on the copy (or the scratch database): one
//     connection with statement_timeout / lock_timeout set.
//   - [SplitStatements] splits a SQL file the way postgres reads it.
//   - [AnalyzeMigrations] applies migrations as forge's migrator does — each
//     file in a transaction that commits, a statement that cannot be in one
//     on its own — measures each statement (pg_locks, pg_class.relfilenode,
//     pg_stat_xact_user_tables, wall time) and applies the rules. The
//     migrated schema stays in the copy for a later pass (EXPLAIN) until the
//     caller drops it.
//   - [Document] and its parts are the JSON document.
//
// Design: docs/proposals/db-analyze.md in the forge repository.
package dbanalyze
