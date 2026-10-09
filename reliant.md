IMPORTANT: The current status of the project is that we haven't launched. Thus we never require backwards compatability, and all changes should result in removing old code paths.

IMPORTANT: **we always** need to recover gracefully, to allow conversations to continue.

## Engineering disposition

For consequential decisions — architecture, public APIs, schemas, lifecycle, anything that ships and is hard to change later — reason to the production-grade, durable solution first, not the quick fix. Before building a non-trivial design, name the realistic alternatives and the trade-off, and devil's-advocate the chosen approach **including your own proposals**; bias toward the coherent design that won't hit a ceiling.

**Don't manufacture urgency.** There is almost never real time pressure — user frustration usually means you haven't converged on the right thing, not "go faster / cut corners." Commit to the right design and ship it decisively; never rationalize a hack with invented pressure. When tempted by a shortcut, the question is "is this correct?", not "is this faster?"

Skip this depth for trivial/mechanical work — there, just do it.

## Architecture modes

Reliant runs in two modes: **distributed** and **monolith**. They share most of the same code paths, so changes should preserve parity unless a mode-specific difference is intentional.

We optimize for **distributed** mode:

- Reliant is a **multi-tenant distributed system**; we use **NATS** to send messages to the tools daemon.
- The **API server**, **worker**, and **daemon gateway** do **not** have access to the user's filesystem. Only the **daemon** does.
- The **daemon may not run on the same device as the user**, so do not assume local-device or same-machine access patterns outside the daemon boundary.

## Running reliant to build reliant

There might be 10 reliant processes running at a time, each for a different feature, with dynamically allocated ports (`cat .dev-ports.sh` or `./.reliant/tools/port-info.sh`). We typically use 1 central reliant to iterate on all of the others.

- You **cannot** simply kill or pkill reliant processes — you might be killing yourself, not the intended task.
- Air and Vite hot-reload, so you typically don't need to kill anything, **ever**.
- You are often working in a worktree: keep edits in that worktree. When looking for a file, check both your worktree and the main git worktree.

## Database (Postgres only)

- Migrations live in `internal/db/migrations/postgres`. Create them with `goose -dir internal/db/migrations/postgres create <name> sql` — never hand-write the version (a zeroed `HHMMSS` collides across branches).
- Update `internal/db/postgres/schema.sql` and regenerate sqlc (`make sqlc`) when contracts change; keep generated/query artifacts in sync.
- Key tables: `chats`, `messages`, `message_content_blocks` (text, tool calls), `workflows`, `projects`, `worktrees`.
- Two dev Postgres servers — not drift, different stacks: **5433** is this repo's own docker-compose (`make postgres-up`, `scripts/dev.sh`, the Make test targets); **5434** is the control-plane `forge env up` stack's, database `reliant`, holding real data — read-only, never drop or mutate it.

## Logs

For the usual `forge env up` stack, every log — backend, browser console, Electron — is under `../control-plane/.forge/logs/dev/` (`reliant-temporal-worker.log` = tool execution / call_llm / spawns, `reliant-api-server.log` = RPC handlers, `frontend_reliant-web.log` = UI `[browser:<level>]` lines), NOT `./data/`. Only this repo's own `scripts/dev.sh` stack logs to `./data/logs*`. Never add another log location. Load the `dev-logs` skill before debugging: full file map, DB queries, and how to prove a sink is live before trusting a zero-hit grep.

## Load these skills before touching their area

- `web-styling` — any styling/layout change under `web/src` (semantic tokens, the `bg-muted` elevation trap).
- `onboarding-flow` — onboarding, or anything that creates a daemon during it (a background effect can complete onboarding with no user action).
- `oauth-provider-login` — Claude/Codex provider OAuth (Electron daemon callback vs `reliant auth serve`).

## Testing while iterating

Run the fast tier, scoped to what you touched:

```bash
make test-short PKGS=./internal/<pkg>/...  # -short, cached, no -race, 60s/pkg
```

- **Never `make test` / `make test-all`** — they run `make stop`, which stops
  other agents' environments. For the full lane run `go test ./internal/<pkg>/...`
  (no `-short`) once at the end; CI runs everything.
- **DB-backed tests use `DATABASE_URL` and only `DATABASE_URL`.** Unset, a bare
  `go test` skips them loudly and connects to nothing; `REQUIRE_TEST_DB=1`
  makes that a failure. The Make test targets pass `DATABASE_URL` for you —
  yours if set, otherwise the compose Postgres on 5433, a server shared with
  everyone on this machine. So give them a database of your own:
  `DATABASE_URL='postgres://postgres:postgres@localhost:<port>/<your_db>?sslmode=disable'`.
  Never 5434 (the control-plane stack's real data).
- `-short` skips DB-backed tests (the shared Postgres is the bottleneck). When
  you change SQL or a repo method:
  `DATABASE_URL=<your db> REQUIRE_TEST_DB=1 make test-short PKGS=...`.
- No `-count=1` or `-race` in the inner loop — they defeat the test cache,
  which is correct for hermetic tests. Never set a private `GOCACHE`.
- The cache only sees files the test process itself reads. A test that execs a
  child reading repo files (not embedded, not in `t.TempDir()`) must skip under
  `testing.Short()` or `os.ReadFile` those inputs itself.
- A new test that takes >2s gets `if testing.Short() { t.Skip("<why>; skipped under -short") }`
  — gate it, never weaken the assertion.
- Anything expected to run >~2 min: `run_in_background` + `shell_wait`.
