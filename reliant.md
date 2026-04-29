IMPORTANT: The current status of the project is that we haven't launched. Thus we never require backwards compatability, and all changes should result in removing old code paths.

IMPORTANT: when doing migrations sqlite does now support ALTER TABLE DROP COLUMN

### Architecture modes

Reliant runs in two modes: **distributed** and **monolith**. They share most of the same code paths, so changes should preserve parity unless a mode-specific difference is intentional.

We optimize for **distributed** mode:

- Reliant is a **multi-tenant distributed system**; we use **NATS** to send messages to the tools daemon.
- The **API server**, **worker**, and **daemon gateway** do **not** have access to the user's filesystem. Only the **daemon** does.
- The **daemon may not run on the same device as the user**, so do not assume local-device or same-machine access patterns outside the daemon boundary.

### Configuration

**Dynamic Ports:** Reliant uses dynamic port allocation to support multiple instances. Ports are discovered at startup and written to `.env.ports`.

### Database

**NOTE**: When checking for files, check both your local worktree, but also the main git worktree.

- **SQLite (default):** `./data/reliant.db` (`DATABASE_DRIVER` unset or `sqlite`)

  ```bash
  sqlite3 ./data/reliant.db ".tables"
  sqlite3 ./data/reliant.db "SELECT * FROM chats LIMIT 5;"
  ```

- **Postgres (optional):** set `DATABASE_DRIVER=postgres` + `DATABASE_URL`
  - Local dev uses shared Docker Postgres at `localhost:5433`
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

#### Dual-Driver Parity Requirement (SQLite + Postgres)

Any DB-related change must keep SQLite and Postgres behavior aligned.

- Update **both** query/repo/mapper paths when one changes
- Add equivalent migrations in:
  - `internal/db/migrations/sqlite`
  - `internal/db/migrations/postgres`
- Keep generated/query artifacts in sync when contracts change
- Validate with tests in both modes (SQLite + Postgres)
- Do not merge one-driver-only DB features unless explicitly intentional

- **Temporal DB:** `./data/temporal.db` (internal state; usually don’t query directly)

### Logs

| Log File                  | Content                                          |
| ------------------------- | ------------------------------------------------ |
| `./data/logs.txt`         | Combined dev server output (Air, Vite, Electron) |
| `./data/logs/reliant.log` | Structured application logs                      |
| `./data/build-errors.log` | Go compilation errors (Air)                      |

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
cat .env.ports
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

### Project nuances

We are using reliant to build reliant. Reliant is a multi-workflow, multi-workspace agentic assistant. That means there might be 10 reliant processes we're running at a time, each for a different feature. Reliant has dynamic port allocation. We typically use 1 central reliant to iterate on all of the others.

- This means you **cannot** simply kill or pkill reliant processes, you might be killing yourself, not the intended task.
- Reliant has air and vite hot reloading, so you typically don't need to kill anything, **ever**
- You are often working in a worktree, retain edits to that workspace.
- The logs, reliant db, and temporal db are all in the current worktree's ./data/ dir.

IMPORTANT: **we always** need to recover gracefully, to allow conversations to continue.