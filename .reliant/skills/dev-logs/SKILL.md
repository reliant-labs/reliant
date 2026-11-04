---
name: dev-logs
description: Where every dev log and dev DB lives (one layout, `forge env up dev` in this checkout or in ../control-plane), how to prove a log sink is live before trusting a zero-hit grep, and Proxyman request correlation. Load before debugging anything in a running dev stack.
---

# Dev logs and databases

## One layout: `.forge/logs/dev/` of the checkout that ran `forge env up dev`

Every dev stack is a `forge env up dev`, and forge tees every process's
stdout — server roles, Vite, the browser console, Electron — into ONE file per
workload under the checkout that started it:

```
<checkout>/.forge/logs/dev/<workload>.log
```

There are two checkouts that can run one. Find out which stack produced the
behaviour you are debugging before you read anything:

| Stack | Started with | Logs | Its reliant database |
| --- | --- | --- | --- |
| **reliant's own** (the product alone: api, worker, gateway, web; no control plane, no k3d) | `forge env up dev` in this checkout (`deploy/kcl/dev/`) | `./.forge/logs/dev/` | forge host Postgres 16, on this worktree's port block; database `reliant` |
| **control-plane's** (the full platform: billing, LLM gateway, managed workspaces, Electron) | `forge env up dev` in `../control-plane` | `../control-plane/.forge/logs/dev/` | control-plane's docker-compose Postgres on **localhost:5434**, the database its `forge env config dev` names (one per worktree). **Real data: read-only, never drop or mutate it** |

Neither port nor database name is fixed: each worktree's stack gets its own
100-port block (`forge env devstack list`). Ask forge, never a file:

```bash
./.reliant/tools/port-info.sh               # this checkout's stack: ports, URLs, database, log dir
./.reliant/tools/port-info.sh --cp          # control-plane's
forge env config dev --workload reliant-api-server         # every variable a process got
forge env config dev --json | jq -r '.workloads[].env.DATABASE_URL // empty' | sort -u
```

| Log file | Content | Stack |
| --- | --- | --- |
| `reliant-temporal-worker.log` | **Activities: tool execution, call_llm, spawns.** Where tool call ids live. Busiest and usually the one you want. | both |
| `reliant-api-server.log` | RPC handlers: interrupt, pause, send, queueing | both |
| `daemon-gateway.log` | Daemon connections + tool routing | both |
| `reliant-migrate.log` | The `reliant db migrate up` job that gates the server roles | both |
| `frontend_web.log` | **The UI.** Every browser `console.*` line, prefixed `[browser:<level>]`, plus uncaught errors and unhandled rejections | reliant's |
| `frontend_reliant-web.log` | The same, for control-plane's stack — Electron's renderer AND a plain browser tab | control-plane's |
| `reliant-electron-main.log` | Electron MAIN process only (Node side: window lifecycle, daemon spawn, IPC). Not the UI. | control-plane's (`-D electron=1`) |
| `reliant-electron.log` | The `npm run dev:electron` process's own stdout, as captured by forge | control-plane's |
| `admin-server.log` | Admin/proxy, auth, billing, **coupons**, daemon create, CompleteOnboarding | control-plane's |

Host infrastructure (reliant's stack) logs to its own data dir:
`.forge/hostinfra/<name>/<engine>.log`.

```bash
grep '\[browser:error\]' .forge/logs/dev/*.log                     # UI errors, this checkout's stack
grep -r "$CHAT_ID"       ../control-plane/.forge/logs/dev/          # one id across control-plane's stack
./.reliant/tools/log-search.sh --errors                             # both of those, per file
./.reliant/tools/log-search.sh --cp "$CHAT_ID"
```

Quick disambiguation — run this before trusting any log grep:

```bash
C=<chat-id>
for f in .forge/logs/dev/*.log ../control-plane/.forge/logs/dev/*.log; do
  n=$(grep -c "$C" "$f" 2>/dev/null); [ "${n:-0}" -gt 0 ] && echo "$f: $n"
done
```

`reliant-dev workflow analyze` and `scripts/wf-supervise` read control-plane's
database because they supervise control-plane-backed runs. A chat that is not
in one stack's database is almost certainly in the other's: pointing a tool at
the wrong stack reads the wrong database, it is not drift.

Tests never touch either database: they boot forge pgtest's embedded Postgres
(or `FORGE_TEST_POSTGRES_URL`) and never read `DATABASE_URL`.

## How the browser half gets there — do not add another log location

`frontends/web/src/lib/browser-log-boot.ts` (imported FIRST in `main.tsx`, before any
other import can log) wraps `console.*` and POSTs to `/__forge/log`; the Vite
plugin in `frontends/web/vite-plugin-browser-logs.ts` prints each line to its own stdout,
which forge already captures. It runs in Electron too — the renderer is the
same origin as a browser tab, so treating them differently is what previously
made the UI go dark with no clue why.

The Electron main process writes to the same directory via `RELIANT_LOG_DIR`,
set in `control-plane/deploy/kcl/dev/main.k` (read at process start, so a change
needs a stack restart). Unset, it falls back to `.reliant/logs/` for a bare
`npm run dev:electron`.

`.reliant/logs/` and the old log files under `data/` were all retired because a
second plausible-looking file is worse than none: greps against it return real
output while silently missing most of the stream, which is indistinguishable
from "my code never ran". A plugin that writes its own file recreates that
failure.

## Before concluding "my logging never ran", prove the sink is live

Zero hits means one of two very different things — the code did not execute,
or you are reading a file nothing writes to — and they are indistinguishable
from the grep alone. Cheapest discriminator, in order:

```bash
# 1. Is the file being written RIGHT NOW? (every log's mtime, and the time now)
./.reliant/tools/log-search.sh --where          # --cp for control-plane's stack

# 2. Does the running server actually serve your edit? (Vite, no rebuild needed)
WEB=$(forge env config dev --json | jq -r '.workloads[] | select(.name == "web") | .env.VITE_APP_URL')
curl -s "$WEB/src/path/to/File.tsx" | grep -c 'your-tag'

# 3. Only then: is the code path reached?
grep -c 'your-tag' <logfile>
```

Step 2 is the one that gets skipped. It separates "my change is not loaded"
from "my change is loaded but that branch never runs" in a single command.
Ports are per worktree: `./.reliant/tools/port-info.sh` prints the web URL.

## Proxyman request correlation

For local debug runs, Proxyman can inject a response header carrying its
internal flow ID, `x-proxyman-id` (from Proxyman script `context.flow.id`).
Debug-only correlation behavior, not a production contract.

1. Find `proxyman_id` / `x-proxyman-id` in the logs:
   `./.reliant/tools/log-search.sh "proxyman_id\|x-proxyman-id"`
2. Open that flow with Proxyman MCP: `mcp__proxyman__get_flow_detail(flow_id="...")`,
   or search: `mcp__proxyman__filter_flows(key="responseHeader", matching="contains", value="x-proxyman-id")`.
