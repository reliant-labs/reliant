IMPORTANT: The current status of the project is that we haven't launched. Thus we never require backwards compatability, and all changes should result in removing old code paths.

IMPORTANT: **we always** need to recover gracefully, to allow conversations to continue.

## Engineering disposition

For consequential decisions — architecture, public APIs, schemas, lifecycle, anything that ships and is hard to change later — reason to the production-grade, durable solution first, not the quick fix. Before building a non-trivial design, name the realistic alternatives and the trade-off, and devil's-advocate the chosen approach **including your own proposals**; bias toward the coherent design that won't hit a ceiling.

**Don't manufacture urgency.** There is almost never real time pressure — user frustration usually means you haven't converged on the right thing, not "go faster / cut corners." Commit to the right design and ship it decisively; never rationalize a hack with invented pressure. When tempted by a shortcut, the question is "is this correct?", not "is this faster?"

Skip this depth for trivial/mechanical work — there, just do it.

## Architecture

Reliant is a **multi-tenant distributed system** with three server roles — **API server**, **worker**, and **daemon gateway** (`reliant server api|worker|gateway`) — plus the **daemon**. There is no all-in-one server process.

- We use **NATS** to send messages to the tools daemon.
- The **API server**, **worker**, and **daemon gateway** do **not** have access to the user's filesystem. Only the **daemon** does.
- The **daemon may not run on the same device as the user**, so do not assume local-device or same-machine access patterns outside the daemon boundary.

## Running reliant to build reliant

`forge env up dev` in this checkout runs the product alone (`deploy/kcl/dev/`): api, worker and gateway under air, the `frontends/web` Vite server, and forge host Postgres 16, NATS and Temporal; no control plane, no docker, no k3d. `forge env up dev` in `../control-plane` is the full platform (billing, LLM gateway, managed workspaces, Electron) and needs k3d. There might be 10 stacks running at a time, one per worktree, each on its own 100-port block with its own data: never assume a port. `./.reliant/tools/port-info.sh` (`--cp` for control-plane's) or `forge env config dev` prints them.

- You **cannot** simply kill or pkill reliant processes — you might be killing yourself, not the intended task. `forge env down dev` stops only the stack of the checkout it runs in.
- Air and Vite hot-reload, so you typically don't need to kill anything, **ever**.
- You are often working in a worktree: keep edits in that worktree. When looking for a file, check both your worktree and the main git worktree.
- `task --list` names every task; each runs the forge `forge.yaml` pins, never whatever `forge` is on PATH.

## Database (Postgres only)

- Migrations are forge migrations in `db/migrations/` (`<YYYYMMDDHHMMSS>_<name>.up.sql`, forward only, applied by forge's migrator). Create them with `task db:migration NAME=<name>` — never hand-write the version. Each one says whether the previous release survives it: `-- forge:backward-compatible — <why>` (the default; expand/contract) or `-- forge:breaks-rollback <why>`, enforced by `forge lint --migration-safety`. One file runs as one transaction; a `CREATE INDEX CONCURRENTLY` must be the only statement in its file (see `internal/db/SCHEMA.md`).
- The forge ORM projects the applied schema into `internal/store`: after a migration run `task generate:forge` (`task generate:check` fails if you forgot) and commit what it writes. Queries live in `internal/store/<entity>_repo_ext.go`; `internal/db` is the hand Repository facade over them (rules: `internal/db/SCHEMA.md`). There is no sqlc.
- Key tables: `chats`, `messages`, `message_content_blocks` (text, tool calls), `workflows`, `projects`, `worktrees`.
- Each dev stack has its own reliant database, named by `forge env config dev`: this checkout's stack runs one on its port block; control-plane's is on its docker-compose Postgres, **localhost:5434**, holding real data — read-only, never drop or mutate it.

## Logs

Every log — backend, browser console, Electron — is under `.forge/logs/dev/` of the checkout that ran `forge env up dev`: `./.forge/logs/dev/` for this checkout's stack, `../control-plane/.forge/logs/dev/` for control-plane's (`reliant-temporal-worker.log` = tool execution / call_llm / spawns, `reliant-api-server.log` = RPC handlers, `frontend_web.log` / `frontend_reliant-web.log` = UI `[browser:<level>]` lines). Never add another log location. Load the `dev-logs` skill before debugging: full file map, DB queries, and how to prove a sink is live before trusting a zero-hit grep. `./.reliant/tools/log-search.sh` greps them.

## Merging to main: GitHub's native merge queue

Two local gates (`dev-docs/merge-and-release-gates.md`): the **merge gate**, `task ci:merge` (`scripts/merge-gate.sh`: affected tests `-short` with direct importers, `go build`, scoped `forge lint`, generate/docs checks when their inputs changed), before you ask for a merge; the **release gate**, `task release:gate`, before every release.

`main` merges only through GitHub's merge queue (ruleset "main: CI result +
merge queue"; never bypass it, no `--admin`). It requires the `CI result` check,
which CI (`.github/workflows/ci.yml`) runs on the queue's own merge group, so the
commit that lands is the commit that was tested. **Review first:** a PR push runs
nothing heavy (`CI result` is green on the PR in about a minute); the merge gate
runs once, in the queue, after `pr-review` passes the PR; the full suite runs on
main after the merge and gates the release, never the merge. Label a PR `ci:run`
(then push) to run the merge gate on it before review. Queue a PR with:

```bash
gh pr merge <pr> --squash --auto --match-head-commit <sha>
```

The PR pipeline does this for you: label the PR `merge:queue` and stop. The
`pr-review` workflow reviews the exact head; on `review:passed` it enqueues it.
`review:changes` means fix, push, and re-add `merge:queue`. Design:
control-plane `docs/pr-pipeline.md`. There is no waiver and no local merge
script any more (`merge_queue3.sh` and `merge_gate_waive.sh` are retired).

## Load these skills before touching their area

- `web-styling` — any styling/layout change under `frontends/web/src` (semantic tokens, the `bg-muted` elevation trap).
- `onboarding-flow` — onboarding, or anything that creates a daemon during it (a background effect can complete onboarding with no user action).
- `oauth-provider-login` — Claude/Codex provider OAuth (Electron daemon callback vs `reliant auth serve`).

## Testing while iterating

Run the affected tier first: it runs ONLY the tests your diff can reach (Go
packages plus their reverse dependencies, `-short`; the vitest files importing a
changed web module; electron when it or a file it reads changed) and says why each ran:

```bash
task test:affected                          # diff vs merge-base with origin/main, all lanes
task test:affected:plan                     # same decision, nothing run
task test:affected LANES=go                 # one lane; TEST_BASE=<rev> moves the base, TEST_FULL=1 runs all
task test:short -- ./internal/<pkg>/...     # one package by hand: -short, cached, no -race
task ci:merge                               # the merge gate, before you hand a branch to the queue
```

`go.mod`, `package.json`, `Taskfile.yml`, config, or a path nothing claims runs the whole
lane. A migration also runs the DB-backed packages it reaches. Rule table and
blind spots: `dev-docs/testing-affected.md`.
Never run the unscoped `npx vitest` for a quick check: ~750 files, minutes under load.

- **Use `task`; there is no Makefile.** Nothing here stops another agent's
  environment. For the full lane of one package run
  `task test -- ./internal/<pkg>/...` (no `-short`, `-race`) once at the end;
  never the unscoped `task test` as a quick check (the whole module, web and
  electron).
- **Two gate tiers.** The merge gate (and the merge queue's CI) runs only what the diff
  reaches, `-short`, direct importers only. The release gate on main runs
  everything in full: `-race`, every slow test, integration, e2e. So a slow
  test, or a package two imports away from your change, runs only there: when
  you touch code one covers, run its package with `task test -- ./<pkg>/...`.
  New and changed tests run 3x (`task test:flake`): a test that fails on a
  repeat is a flake to fix, never to re-run. A red main opens a `ci:main-red`
  issue. Details: `dev-docs/merge-and-release-gates.md`.
- **DB-backed tests need no database from you.** They use forge pgtest's
  shared embedded Postgres (the major `forge.yaml` declares, booted on first
  use and shared by every test process on the machine; each test gets its own
  copy of a migrated template), or `FORGE_TEST_POSTGRES_URL` (a maintenance
  DSN on that major) when you point them at a server of your own. They **never
  read `DATABASE_URL`**, and they run under `-short` too.
- No `-count=1` or `-race` in the inner loop — they defeat the test cache,
  which is correct for hermetic tests. Never set a private `GOCACHE`.
- The cache only sees files the test process itself reads. A test that execs a
  child reading repo files (not embedded, not in `t.TempDir()`) must skip under
  `testing.Short()` or `os.ReadFile` those inputs itself.
- A new test that takes >2s: first make it fast — a fixed wait on a production
  timeout or backoff gets a package-level var or option the test shrinks. Only
  if it is inherently slow (a cluster, a real subprocess) does it call
  `testkit.Slow(t, "<why>")` first, which moves it off the merge gate into the
  release gate. Gate it, never weaken the assertion.
- Anything expected to run >~2 min: `run_in_background` + `shell_wait`.
- **Never start long work with `nohup`, a trailing `&`, `setsid` or `disown`.**
  Use `run_in_background`: the daemon then owns the command's whole process
  group, limits it, and `shell_kill` takes all of it down. What you detach
  yourself is invisible to the daemon and to every other agent. On 2026-10-30
  the shared machine ran out of processes (~5,200 bash) and was restarted,
  killing every agent's work; gates started with `nohup … &` were part of it.
