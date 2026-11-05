---
name: migration-v1.0.0-pgtest-embedded
description: pgtest.StartEmbedded returns *pgtest.Embedded, which owns the embedded-postgres temp log file it created. Use when Go code calls pgtest.StartEmbedded and keeps the result as *embeddedpostgres.EmbeddedPostgres.
relevance: migration
---

# pgtest.StartEmbedded returns *pgtest.Embedded

Use when Go code calls `pgtest.StartEmbedded` and stores or passes its result
as a `*embeddedpostgres.EmbeddedPostgres`.

## What changed

embedded-postgres creates one `embedded_postgres_log*` temp file per Start in
the shared `os.TempDir()` and reopens it by name on every flush (the end of
Start, and inside Stop). `StartEmbedded` used to delete every such file that
appeared during its own Start, which broke any concurrent instance (another
test process) with `unable to process postgres logs: open …: no such file`.
It now returns `*pgtest.Embedded`, which owns exactly the log file its own
Start created.

## What to do

- `ep, err := pgtest.StartEmbedded(cfg)` still compiles; only a declared type
  changes: `*embeddedpostgres.EmbeddedPostgres` becomes `*pgtest.Embedded`
  (`Stop() error` is unchanged and now also removes the log, after the
  library's final flush).
- If the server outlives your handle (stopped later by `pg_ctl`, or by another
  process), call `ep.Detach()` after a successful Start so the log is not
  leaked. The running postmaster is unaffected.
- No codemod: the change is a type at a call site and whether the server
  outlives the handle is a decision about your lifecycle.
