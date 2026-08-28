IMPORTANT: The current status of the project is that we haven't launched. Thus we never require backwards compatability, and all changes should result in removing old code paths.

IMPORTANT: when doing migrations sqlite does now support ALTER TABLE DROP COLUMN

## Engineering disposition

For consequential decisions — architecture, public APIs, schemas, lifecycle, anything that ships and is hard to change later — reason to the production-grade, durable solution first, not the quick fix. Before building a non-trivial design, name the realistic alternatives and the trade-off, and devil's-advocate the chosen approach **including your own proposals**; bias toward the coherent design that won't hit a ceiling.

**Don't manufacture urgency.** There is almost never real time pressure — user frustration usually means you haven't converged on the right thing, not "go faster / cut corners." Commit to the right design and ship it decisively; never rationalize a hack with invented pressure. When tempted by a shortcut, the question is "is this correct?", not "is this faster?"

Skip this depth for trivial/mechanical work — there, just do it.

### Architecture modes

Reliant runs in two modes: **distributed** and **monolith**. They share most of the same code paths, so changes should preserve parity unless a mode-specific difference is intentional.

We optimize for **distributed** mode:

- Reliant is a **multi-tenant distributed system**; we use **NATS** to send messages to the tools daemon.
- The **API server**, **worker**, and **daemon gateway** do **not** have access to the user's filesystem. Only the **daemon** does.
- The **daemon may not run on the same device as the user**, so do not assume local-device or same-machine access patterns outside the daemon boundary.

### Configuration

**Dynamic Ports:** Reliant uses dynamic port allocation to support multiple instances. Ports are discovered at startup and written to `.dev-ports.sh`.

### Database

**NOTE**: When checking for files, check both your local worktree, but also the main git worktree.

- **SQLite (default):** `./data/reliant.db` (`DATABASE_DRIVER` unset or `sqlite`)

  ```bash
  sqlite3 ./data/reliant.db ".tables"
  sqlite3 ./data/reliant.db "SELECT * FROM chats LIMIT 5;"
  ```

- **Postgres (optional):** set `DATABASE_DRIVER=postgres` + `DATABASE_URL`
  - This repo's own `docker-compose.yml` Postgres, published on `localhost:5433`
    (`make postgres-up`). `scripts/dev.sh` and `make test-e2e` target this one.
  - Not to be confused with the control-plane dev stack's Postgres on
    `localhost:5434`, a **separate server** that also hosts a `reliant`
    database. `reliant-dev workflow analyze` and `scripts/wf-supervise` read
    that one, because they supervise runs of a control-plane-backed stack. The two
    ports are not drift — pointing either set at the other's port reads the
    wrong database.
  - `scripts/dev.sh` / `scripts/dev.ps1` auto-provision a **per-worktree DB**
  ```bash
  PGPASSWORD=postgres psql -h localhost -p 5433 -U postgres -d "$PGDATABASE" -c "\dt"
  PGPASSWORD=postgres psql -h localhost -p 5433 -U postgres -d "$PGDATABASE" -c "SELECT * FROM chats LIMIT 5;"
  # or
  psql "$DATABASE_URL" -c "\dt"
  psql "$DATABASE_URL" -c "SELECT * FROM chats LIMIT 5;"
  ```

**Key tables (both drivers):**
| Table | Purpose |
|-------|---------|
| `chats` | Chat sessions with workflow associations |
| `messages` | All messages (user, assistant, tool) |
| `message_content_blocks` | Message content parts (text, tool calls) |
| `workflows` | Workflow execution records |
| `projects` | Project configurations |
| `worktrees` | Git worktree associations |

#### Database Driver (Postgres only)

Postgres is the only supported database driver. SQLite support has been removed.

- Add migrations in `internal/db/migrations/postgres`
- Update `internal/db/postgres/schema.sql` and regenerate `sqlc` code (`make sqlc`) when contracts change
- Keep generated/query artifacts in sync when contracts change

- **Temporal DB:** `./data/temporal.db` (internal state; usually don’t query directly)

### Logs

**Everything the `forge env up` stack produces — backend, frontend, Electron —
is in ONE directory:**

```
../control-plane/.forge/logs/dev/
```

Start every log question there. The only other location is the legacy
`scripts/dev.sh` stack at the bottom of this section, which you are almost
certainly not running.

**Control-plane-backed stack (`forge env up` — the usual one)** — logs go to
`control-plane/.forge/logs/dev/`, NOT to this worktree's `./data/`:

| Log File                                                 | Content                                         |
| -------------------------------------------------------- | ----------------------------------------------- |
| `control-plane/.forge/logs/dev/reliant-temporal-worker.log` | **Activities: tool execution, call_llm, spawns.** Where tool call ids live. Busiest and usually the one you want. |
| `control-plane/.forge/logs/dev/reliant-api-server.log`      | RPC handlers: interrupt, pause, send, queueing   |
| `control-plane/.forge/logs/dev/frontend_reliant-web.log`    | **The UI.** Every browser/renderer `console.*` line, prefixed `[browser:<level>]`, plus uncaught errors and unhandled rejections. Anything you add to `web/src` lands HERE — in Electron AND a plain browser tab. |
| `control-plane/.forge/logs/dev/reliant-electron-main.log`   | Electron MAIN process only (Node side: window lifecycle, daemon spawn, IPC). Not the UI. |
| `control-plane/.forge/logs/dev/reliant-electron.log`        | The `npm run dev:electron` process's own stdout, as captured by forge. |
| `control-plane/.forge/logs/dev/admin-server.log`            | Admin/proxy, auth, billing, **coupons**, daemon create, CompleteOnboarding |
| `control-plane/.forge/logs/dev/daemon-gateway.log`          | Daemon connections + tool routing (absent when not running) |

**ALL dev logging is under `control-plane/.forge/logs/dev/`. There is no
second location.** `forge env up` tees every host process's stdout there, so
one directory and one grep covers the whole stack:

```bash
grep '\[browser:'        ../control-plane/.forge/logs/dev/frontend_reliant-web.log
grep '\[browser:error\]' ../control-plane/.forge/logs/dev/*.log   # UI errors, any frontend
grep -r "$CHAT_ID"       ../control-plane/.forge/logs/dev/        # across the stack
```

How the browser half gets there: `web/src/lib/browser-log-boot.ts` (imported
FIRST in `main.tsx`, before any other import can log) wraps `console.*` and
POSTs to `/__forge/log`; the Vite plugin in `web/vite-plugin-browser-logs.ts`
prints each line to its own stdout, which forge already captures. It runs in
Electron too — the renderer is the same origin as a browser tab, so treating
them differently is what previously made the UI go dark with no clue why.

The Electron main process writes to the same directory via `RELIANT_LOG_DIR`,
set in `control-plane/deploy/kcl/dev/main.k`. Unset, it falls back to
`.reliant/logs/` for a bare `npm run dev:electron`.

**Do not add another log location.** `.reliant/logs/` and `data/logs/browser.log`
were both retired precisely because a second plausible-looking file is worse
than none: greps against it return real output while silently missing most of
the stream, which is indistinguishable from "my code never ran".

Pair it with the matching DB: that stack's chats/tool_calls are in the
**control-plane postgres on port 5434**, database `reliant` (read-only for
debugging — it holds real data, never drop or mutate it). A chat that is NOT in
5433 is almost certainly here.

**This worktree's own `scripts/dev.sh` stack** — logs stay local:

| Log File                  | Content                                          |
| ------------------------- | ------------------------------------------------ |
| `./data/logs.txt`         | Combined dev server output (Air, Vite, Electron) |
| `./data/logs/reliant.log` | Structured application logs                      |
| `./data/build-errors.log` | Go compilation errors (Air)                      |

Its DB is the per-worktree postgres on **5433**.

Quick disambiguation — run this before trusting any log grep:

```bash
C=<chat-id>
for f in ../control-plane/.forge/logs/dev/reliant-temporal-worker.log \
         ../control-plane/.forge/logs/dev/reliant-api-server.log \
         ../control-plane/.forge/logs/dev/reliant-electron.log \
         ./data/logs/reliant.log; do
  printf "%s: %s\n" "$f" "$(grep -c "$C" "$f" 2>/dev/null)"
done
```

**Before concluding "my logging never ran", prove the sink is live.** Zero
hits means one of two very different things — the code did not execute, or
you are reading a file nothing writes to — and they are indistinguishable
from the grep alone. Cheapest discriminator, in order:

```bash
# 1. Is the file being written RIGHT NOW?
stat -f "%Sm" -t "%H:%M:%S" <logfile>; date "+now      %H:%M:%S"

# 2. Does the running server actually serve your edit? (Vite, no rebuild needed)
curl -s http://127.0.0.1:$FRONTEND_PORT/src/path/to/File.tsx | grep -c 'your-tag'

# 3. Only then: is the code path reached?
grep -c 'your-tag' <logfile>
```

Step 2 is the one that gets skipped. It separates "my change is not loaded"
from "my change is loaded but that branch never runs" in a single command,
and it is the difference between debugging the bug and debugging your own
tooling for an afternoon.

### Proxyman request correlation (debug workflow)

Use Proxyman to make request/response correlation easy from logs to captured flows.

- For local debug runs, Proxyman can inject a response header carrying its internal flow ID:
  - `x-proxyman-id` (from Proxyman script `context.flow.id`)
- This is debug-only correlation behavior (not a production contract).

#### Correlation workflow

1. Find `proxyman_id` (or `x-proxyman-id`) in `./data/logs/reliant.log` (or `./data/logs.txt`)
2. Open that flow directly with Proxyman MCP `get_flow_detail(flow_id="...")`

#### Fast lookup examples

```bash
# Structured log lookup
rg "proxyman_id|x-proxyman-id" ./data/logs/reliant.log ./data/logs.txt
```

MCP equivalents:

- `mcp__proxyman__get_flow_detail(flow_id)`
- `mcp__proxyman__filter_flows(key="responseHeader", matching="contains", value="x-proxyman-id")`

### OAuth (Claude / Codex provider login)

Claude and Codex OAuth flows require a localhost callback receiver. The architecture differs by runtime:

- **Electron**: The daemon handles it automatically via `auth.start_oauth` — no user action needed.
- **Web browser**: There is no local daemon. Users must run `reliant auth serve` in their terminal, which starts a lightweight HTTP server on `localhost:19284` with:
  - `GET /health` — frontend pings this to detect availability
  - `POST /oauth/start` — receives authorize URL template, opens browser, waits for callback, returns auth code

The `auth serve` server is **stateless** — it only bridges the localhost callback gap. Token exchange and persistence always go through the authenticated backend gRPC (`completeClaudeOAuth` / `completeCodexOAuth`).

Key files:
- `internal/auth/oauthcallback/` — shared Go package for OAuth callback handling (used by both daemon and `auth serve`)
- `cmd/reliant/commands/auth_serve.go` — the `reliant auth serve` CLI command
- `web/src/lib/oauth-local.ts` — frontend helper to call the local server
- `web/src/hooks/useOAuthAvailability.ts` — hook that checks if OAuth is available (Electron → always, web → pings health)
- `web/src/lib/claude-oauth.ts` / `codex-oauth.ts` — OAuth flow libs that branch between daemon (Electron) and local server (web)

### Ports

Ports are dynamically assigned. Check current ports:

```bash
cat .dev-ports.sh
# or
./.reliant/tools/port-info.sh
```

### Web styling contract

Use the standard Tailwind + CSS variable token path for UI styling:

- Prefer semantic Tailwind classes such as `bg-background`, `text-foreground`, `text-muted-foreground`, `border-border`, `bg-primary`, `text-primary-foreground`, `text-destructive`, and component variants over inline token styles.
- Use `cn()` for conditional classes; do not mutate DOM styles in hover/focus handlers when Tailwind state variants can express the state.
- Theme palettes are controlled by `data-color-scheme`; light/dark mode is controlled by the `.dark` class. Do not add new `data-theme` selectors.
- Keep new shared styling in component primitives under `web/src/components/ui/` or token variables in CSS, not deep ad-hoc selector chains.
- For workflow config panel styles, add explicit `cpv2-*` ownership classes at the rendered component boundary instead of overriding shared primitives or targeting nested generated markup.
- Inline styles are acceptable for runtime geometry, Electron drag regions (`WebkitAppRegion`), dynamic SVG/data colors, and third-party APIs that require style objects.
- Avoid broad `!important`, magic negative margins, incidental parent selectors, hardcoded brand colors, and arbitrary CSS values unless the exception is documented near the use.
- `!important` is acceptable only for browser quirks or generated third-party DOM such as WebKit autofill, Monaco, XTerm, and ReactFlow; keep selectors scoped and prefer component props/classes first.
- Run `npm run lint:css` from `web/` when changing stylesheets; keep the Stylelint config focused on correctness/hygiene rather than formatting churn.
- When changing tokens or color-scheme CSS, run the color-scheme contract test and a web build path when practical.

---

### Onboarding: the step you see is derived, and a background effect can end the flow

`deriveStep(plan)` (`web/src/components/OnboardingFlow/stepConfig.ts`) is the
ONLY thing that decides which step renders — from the `plan` search param in
the URL, nothing else. `onNext()` is a no-op. So "it skipped steps" is always
a question about what wrote `plan`, or about whether the flow was exited
entirely.

**`OnboardingRoute.tsx` can complete onboarding on its own, from a
`useEffect`, with no user action.** The returning-user heal fires when
`hasUsableControlPlaneDaemonForOnboarding(daemonsPostdating(daemons, user.createdAtMs))`
is true — a daemon that postdates the account — and it calls
`CompleteOnboarding` and navigates to `/`. Creating a daemon DURING onboarding
satisfies that condition, so any change that provisions a daemon mid-flow can
trip the heal and end onboarding rather than advance it. Verified in
`admin-server.log`: `daemon created name=onboarding-daemon` at 17:24:40.809,
`onboarding completed` 39ms later, with no step in between.

When debugging this flow, read `admin-server.log` first — the RPC sequence
(`RedeemCoupon` → `CreateDaemon` → `CompleteOnboarding`) tells you what
actually happened server-side, independent of any frontend logging.

### Project nuances

We are using reliant to build reliant. Reliant is a multi-workflow, multi-workspace agentic assistant. That means there might be 10 reliant processes we're running at a time, each for a different feature. Reliant has dynamic port allocation. We typically use 1 central reliant to iterate on all of the others.

- This means you **cannot** simply kill or pkill reliant processes, you might be killing yourself, not the intended task.
- Reliant has air and vite hot reloading, so you typically don't need to kill anything, **ever**
- You are often working in a worktree, retain edits to that workspace.
- The logs, reliant db, and temporal db are all in the current worktree's ./data/ dir.

IMPORTANT: **we always** need to recover gracefully, to allow conversations to continue.