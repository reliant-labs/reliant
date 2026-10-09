---
name: dev-logs
description: Where every dev log and dev DB lives (forge env up stack vs scripts/dev.sh), how to prove a log sink is live before trusting a zero-hit grep, and Proxyman request correlation. Load before debugging anything in a running dev stack.
---

# Dev logs and databases

## Two stacks, two places

**Control-plane-backed stack (`forge env up` from `../control-plane` — the usual
one).** Everything it produces — backend, frontend, Electron — is in ONE
directory, NOT this worktree's `./data/`:

```
../control-plane/.forge/logs/dev/
```

| Log file | Content |
| --- | --- |
| `reliant-temporal-worker.log` | **Activities: tool execution, call_llm, spawns.** Where tool call ids live. Busiest and usually the one you want. |
| `reliant-api-server.log` | RPC handlers: interrupt, pause, send, queueing |
| `frontend_reliant-web.log` | **The UI.** Every browser/renderer `console.*` line, prefixed `[browser:<level>]`, plus uncaught errors and unhandled rejections — Electron AND a plain browser tab. |
| `reliant-electron-main.log` | Electron MAIN process only (Node side: window lifecycle, daemon spawn, IPC). Not the UI. |
| `reliant-electron.log` | The `npm run dev:electron` process's own stdout, as captured by forge. |
| `admin-server.log` | Admin/proxy, auth, billing, **coupons**, daemon create, CompleteOnboarding |
| `daemon-gateway.log` | Daemon connections + tool routing (absent when not running) |

Its DB: the control-plane docker-compose Postgres on **localhost:5434**,
database `reliant` (chats, tool_calls, …). Read-only for debugging — it holds
real data; never drop or mutate it.

**This worktree's own `scripts/dev.sh` stack** — logs stay local:

| Log file | Content |
| --- | --- |
| `./data/logs.txt` | Combined dev server output (Air, Vite, Electron) |
| `./data/logs/reliant.log` | Structured application logs |
| `./data/build-errors.log` | Go compilation errors (Air) |

Its DB: this repo's docker-compose Postgres on **localhost:5433**
(`make postgres-up`); `scripts/dev.sh` auto-provisions a per-worktree database.
`reliant-dev workflow analyze` and `scripts/wf-supervise` read 5434 because they
supervise control-plane-backed runs. The two ports are not drift — pointing a
tool at the other one reads the wrong database. A chat that is not in 5433 is
almost certainly in 5434.

```bash
grep '\[browser:error\]' ../control-plane/.forge/logs/dev/*.log   # UI errors, any frontend
grep -r "$CHAT_ID"       ../control-plane/.forge/logs/dev/        # one id across the stack
psql "postgres://postgres:postgres@localhost:5434/reliant" -c "SELECT id, state FROM chats ORDER BY created_at DESC LIMIT 5;"
```

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

## How the browser half gets there — do not add another log location

`web/src/lib/browser-log-boot.ts` (imported FIRST in `main.tsx`, before any
other import can log) wraps `console.*` and POSTs to `/__forge/log`; the Vite
plugin in `web/vite-plugin-browser-logs.ts` prints each line to its own stdout,
which forge already captures. It runs in Electron too — the renderer is the
same origin as a browser tab, so treating them differently is what previously
made the UI go dark with no clue why.

The Electron main process writes to the same directory via `RELIANT_LOG_DIR`,
set in `control-plane/deploy/kcl/dev/main.k` (read at process start, so a change
needs a stack restart). Unset, it falls back to `.reliant/logs/` for a bare
`npm run dev:electron`.

`.reliant/logs/` and `data/logs/browser.log` were both retired because a second
plausible-looking file is worse than none: greps against it return real output
while silently missing most of the stream, which is indistinguishable from "my
code never ran". A plugin that writes its own file recreates that failure.

## Before concluding "my logging never ran", prove the sink is live

Zero hits means one of two very different things — the code did not execute,
or you are reading a file nothing writes to — and they are indistinguishable
from the grep alone. Cheapest discriminator, in order:

```bash
# 1. Is the file being written RIGHT NOW?
stat -c "%y" <logfile>; date        # macOS: stat -f "%Sm" -t "%H:%M:%S" <logfile>

# 2. Does the running server actually serve your edit? (Vite, no rebuild needed)
curl -s http://127.0.0.1:$FRONTEND_PORT/src/path/to/File.tsx | grep -c 'your-tag'

# 3. Only then: is the code path reached?
grep -c 'your-tag' <logfile>
```

Step 2 is the one that gets skipped. It separates "my change is not loaded"
from "my change is loaded but that branch never runs" in a single command.
Ports are dynamic: `cat .dev-ports.sh` or `./.reliant/tools/port-info.sh`.

## Proxyman request correlation

For local debug runs, Proxyman can inject a response header carrying its
internal flow ID, `x-proxyman-id` (from Proxyman script `context.flow.id`).
Debug-only correlation behavior, not a production contract.

1. Find `proxyman_id` / `x-proxyman-id` in the logs:
   `rg "proxyman_id|x-proxyman-id" ./data/logs/reliant.log ./data/logs.txt`
2. Open that flow with Proxyman MCP: `mcp__proxyman__get_flow_detail(flow_id="...")`,
   or search: `mcp__proxyman__filter_flows(key="responseHeader", matching="contains", value="x-proxyman-id")`.
