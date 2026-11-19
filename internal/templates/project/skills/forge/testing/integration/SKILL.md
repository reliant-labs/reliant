---
name: integration
description: "Integration tests — real database, build tags, transaction isolation."
---

# Integration Tests

## Overview

Integration tests run against a **real database** and verify that queries, migrations, and data access layers work correctly. They are invisible to plain `go test` because they require a build tag.

## Build Tag

Every integration test file must include the build constraint at the top:

```go
//go:build integration

package handlers_test
```

This keeps them out of `go test ./...` and ensures they only run when explicitly requested.

## Convention: build tag, never `testing.Short()`

Forge integration tests use the **`//go:build integration`** build tag — *not* `testing.Short()` with `t.Skip(...)`. The two mechanisms answer different questions:

| Mechanism | What it does | Use when |
| --- | --- | --- |
| `//go:build integration` | The Go toolchain physically excludes the file from `go build` / `go test` unless `-tags integration` is passed. No compile, no link, no skip-at-runtime. | The test needs infra that may not be present (DB, broker, network). The default test run must not even attempt it. |
| `if testing.Short() { t.Skip(...) }` | The test is always compiled and runs by default. `go test -short` skips it at runtime. | The test is hermetic but optionally slow — e.g. a fast unit test that has an extra long-running scenario behind `-short`. |

**Rule for forge projects:** all DB-backed / RPC-roundtrip / infra-dependent tests use the build tag. Reserve `testing.Short()` for the rare case of a hermetic test with a fast/slow toggle. Spell that toggle `testkit.Slow(t, "<why>")` (see `testing`): it also skips in the integration lane, so never call it from an integration-tagged test — that test would run nowhere. If your test would `t.Skip` because it can't reach a database, it belongs behind `//go:build integration` instead.

The matching convention for end-to-end tests is `//go:build e2e`.

## Running

```bash
task test:integration                                   # all integration tests
task test:integration -- ./internal/handlers/<name>/... # one service only
task test:all                                           # unit + frontend + integration
```

`task test:integration` adds `-tags integration` to the `go test` invocation for
you; it is defined in the project's `Taskfile.yml`.

Note that `task coverage` does **not** include this lane — it profiles the
untagged suite only. Integration tests are excluded from `task test` by the
build tag, which means they are never compiled there, not skipped at runtime.

## Prerequisites

Integration tests require a running Postgres instance via docker-compose. If you're already running the stack, you're set:

```bash
forge env up dev    # starts the full stack including postgres
```

## Harness primitives: don't hand-roll the DB setup

`pkg/testkit` ships the harness pieces an integration test needs — a migrated test DB, authed contexts, claims options — and `pkg/tdd` exposes table-driven entry points on top (`tdd.SetupMockDB`, `app.NewMigratedTestDB`). Reach for those before hand-rolling connection + migration boilerplate. See the `testing` and `testing/patterns` skills for the cheat sheet.

Mock discipline at this tier: the subject and its real internal collaborators (including the DB) stay real — that's the whole point of integration. Mock only third-party boundaries you don't own.

## Transaction-Per-Test Isolation

Each test runs inside a database transaction that is rolled back in cleanup. This guarantees complete state isolation between tests:

```go
func TestCreateUser(t *testing.T) {
    tx := testdb.Begin(t)       // starts transaction
    t.Cleanup(func() { tx.Rollback() })
    queries := db.New(tx)
    // ... test with real queries
}
```

## Testing Hand-Written Queries

Integration tests are the right place to verify the SQL in `internal/db/<entity>_repo_ext.go` (bun queries and raw statements) against the real schema. Nothing checks that SQL at generate time, so every such query needs one. Test the actual SQL; don't mock the database when the query's behavior is what you're validating.

## Seed Data

- Create test data in the test function or a helper called from the test
- Always clean up with `t.Cleanup()` (transaction rollback handles this automatically)
- **Never rely on test ordering** — each test must set up its own state

## Rules

- **No mocking the DB** — if you're testing queries, use the real database
- **Deterministic assertions** — don't assert on auto-generated IDs or timestamps without pinning them
- **Don't share state** — no package-level variables mutated across tests
- If a test doesn't need the database, it belongs at the unit level
