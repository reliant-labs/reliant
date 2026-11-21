---
name: integration
description: "Integration tests — which tests take the build tag (infrastructure beyond Postgres) and which stay in the fast tier (pgtest), per-test database clones."
---

# Integration Tests

## Overview

Two lanes test against real infrastructure, and the database is not what decides between them.

- **A real Postgres is fast-tier.** `forge/pkg/pgtest` (behind `testkit.NewPostgresDB` and the generated `NewMigratedTestDB`) boots one embedded server per machine and hands each test a clone of a migrated template, so a DB-backed test is untagged and runs in `task test:short` and `task test`. The scaffolded CRUD lifecycle test (`internal/handlers/<svc>/handlers_crud_test.go`) is one: untagged, real Postgres, fast tier. No docker and no running stack are needed. The `testing` skill has the details.
- **The integration lane is for infrastructure pgtest cannot give you**: a container the test starts (a broker, MinIO, a second database engine), a real external API, or the running dev stack. Those tests sit behind `//go:build integration` and run in `task test:integration`.

## Build Tag

An integration test file starts with the build constraint:

```go
//go:build integration

package handlers_test
```

This keeps it out of `go test ./...`, so it runs only when the lane asks for it.

## Convention: build tag, never `testing.Short()`

For tests that need infrastructure the test process cannot start for itself, use the **`//go:build integration`** build tag, *not* `testing.Short()` with `t.Skip(...)`. The two mechanisms answer different questions:

| Mechanism | What it does | Use when |
| --- | --- | --- |
| `//go:build integration` | The Go toolchain physically excludes the file from `go build` / `go test` unless `-tags integration` is passed. No compile, no link, no skip-at-runtime. | The test needs infrastructure that may not be present (a broker, a container, the network). The default test run must not even attempt it. |
| `if testing.Short() { t.Skip(...) }` | The test is always compiled and runs by default. `go test -short` skips it at runtime. | The test is hermetic but optionally slow — e.g. a fast unit test that has an extra long-running scenario behind `-short`. |

**Rule for forge projects:** a test against Postgres uses pgtest and stays untagged. A test against any other infrastructure uses the build tag. Reserve `testing.Short()` for a hermetic test with a fast/slow toggle, and spell it `testkit.Slow(t, "<why>")` (see `testing`): it also skips in the integration lane, so never call it from an integration-tagged test, which would then run nowhere. If your test would `t.Skip` because it cannot reach a service, it belongs behind `//go:build integration` instead.

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

A DB-backed test needs nothing running: pgtest boots its own embedded Postgres (or uses `FORGE_TEST_POSTGRES_URL` when you point it at a server of your own). An integration test needs whatever infrastructure it talks to; a test that drives the dev stack needs it up:

```bash
forge env up dev    # starts the full stack
```

## Harness primitives: don't hand-roll the DB setup

`pkg/testkit` ships the harness pieces an integration test needs — a migrated test DB, authed contexts, claims options — and `pkg/tdd` exposes table-driven entry points on top (`tdd.SetupMockDB`, `app.NewMigratedTestDB`). Reach for those before hand-rolling connection + migration boilerplate. See the `testing` and `testing/patterns` skills for the cheat sheet.

Mock discipline at this tier: the subject and its real internal collaborators (including the DB) stay real — that's the whole point of integration. Mock only third-party boundaries you don't own.

## Per-test isolation

Each test gets its own database: pgtest clones the migrated template for every call, so tests never see each other's rows and need no rollback or cleanup of their own:

```go
func TestCreateUser(t *testing.T) {
    db := app.NewMigratedTestDB(t) // a fresh clone of the migrated schema, dropped at cleanup
    // ... drive the handler or the store against db
}
```

## Testing Hand-Written Queries

Integration tests are the right place to verify the SQL in `internal/db/<entity>_repo_ext.go` (bun queries and raw statements) against the real schema. Nothing checks that SQL at generate time, so every such query needs one. Test the actual SQL; don't mock the database when the query's behavior is what you're validating.

## Seed Data

- Create test data in the test function or a helper called from the test
- Each test's database is its own clone, dropped when the test ends, so no manual cleanup is needed
- **Never rely on test ordering** — each test must set up its own state

## Rules

- **No mocking the DB** — if you're testing queries, use the real database
- **Deterministic assertions** — don't assert on auto-generated IDs or timestamps without pinning them
- **Don't share state** — no package-level variables mutated across tests
- If a test doesn't need the database, it belongs at the unit level
