# Triage briefing — 2026-08-17

Shared context for three parallel investigations. Facts marked **VERIFIED** were
established by direct probing before the fan-out — do **not** re-derive them.
Facts marked **OPEN** are the actual work.

Do not delete this file. The parent thread owns cleanup.

## Repo layout (VERIFIED)

Multi-repo workspace at `/Users/seanteeling/src/reliant-labs/`:

| Path | Module | Role |
|------|--------|------|
| `reliant/` | `github.com/reliant-labs/reliant` | the product — chats, threads, workflows, web UI, electron |
| `control-plane/` | `github.com/reliant-labs/control-plane` | forge app — billing, daemon gateway, admin. **Owns `Taskfile.yml`** |
| `forge/` | `github.com/reliant-labs/forge` | the generator/framework |

`reliant/` has **no Taskfile** — it uses `Makefile` + `package.json` scripts.
Every `task <x>` command in this workspace resolves to
`control-plane/Taskfile.yml`. (VERIFIED: `find -name 'Taskfile*'` returns only
`control-plane/Taskfile.yml` and `forge/Taskfile.yml`.)

## Databases (VERIFIED — this is a common source of wasted turns)

Two separate Postgres servers. They both have a database named `reliant`, and
only one of them has the product tables:

- **port 5434** — control-plane dev stack. Its `reliant` DB is **the live one**:
  `threads` has 1364 rows, newest `2026-08-17 17:17`. Query it with:
  `PGPASSWORD=postgres psql -h localhost -p 5434 -U postgres -d reliant`
- **port 5433** — reliant's own docker-compose. Its `reliant` DB does **not**
  have a `threads` table. Wrong database. Do not use it for thread queries.

Relevant schema (VERIFIED, `psql \d`):

```
threads(id, chat_id, parent_thread_id, workflow_id, created_at, title,
        origin, origin_node_id, status, completed_at, fork_at_message_id)
messages(id, chat_id, ordinal, thread_id, context_window_id, role,
         display_style, model, agent, token_count, cost, workflow_id, run_id,
         node_id, node_path, activity_id, created_at, updated_at, seq)
message_content_blocks(id, message_id, position, block_type, content,
         tool_name, tool_input, tool_call_id, is_error, version, node_id,
         node_path, activity_id, workflow_run_id, attempt_number,
         thought_signature, created_at, updated_at)
```

`origin` is a text column; spawned sub-agent threads have `origin='spawn'`.
A forked thread has `origin='fork'`. `role`: 1=user, 2=assistant, 4=tool-result.
`block_type`: 1=text, 2=tool-call, 3=tool-result.

Useful one-liner (already validated) to trace a sub-agent's tool sequence:

```sql
select m.ordinal, m.role, b.block_type, coalesce(b.tool_name,'-'),
       left(regexp_replace(coalesce(b.content,b.tool_input,''),'\s+',' ','g'),110)
from messages m join message_content_blocks b on b.message_id = m.id
where m.thread_id = '<thread-id>' order by m.ordinal, b.position;
```

## Recent commits (VERIFIED)

Branch: `fix/vite-base-absolute-deep-routes`. There is a large set of
uncommitted changes in the tree (see `git status`) — **other agents are working
in this same checkout**. Never `git stash`, `git checkout <branch>`,
`git reset`, or `git add -A`. Do not run any git command that mutates state.

Commits in the last ~10 days touching the suspect areas:

```
65ce8cb7 refactor async spawn                  <-- large; touches web/src/components/Chat + chatStore + InterleavedTimeline + internal/llm + workflow runtime
8769b3d1 reliant: wake parents on agent messages, surface compute eligibility (#146)
f61d0406 fix: unblock the web build, restore the release workflow, lint via forge
e12802a1 Bump forge (#143)
918a230d reliant: thread lifecycle repair and workflow history reduction (#142)
2c9cd4c1 remove somme docs (#140)
5b78bc43 reliant: agent message mailbox, queued composer input, spawn lifecycle (#138)
```

`65ce8cb7 "refactor async spawn"` is the prime suspect for BOTH the scroll
jitter and the sub-agent regression — it is the most recent commit and it
touched the timeline renderer, the chat store, the streaming reducers, AND the
spawn/prompt path. Its web diffstat (VERIFIED):

```
web/src/components/Chat/ChatMessagesContainer.tsx        154 +-
web/src/components/Chat/ChatPresenter.tsx                121 +-
web/src/components/Chat/thread-views/InterleavedTimeline.tsx  220 +-
web/src/store/chatStore.ts                               255 +-
web/src/lib/chatStreamReducers.ts                         56 +-
web/src/lib/scrollDebug.ts                               439 ++ (new)
web/src/lib/constants.ts                                  20 +-
```

Note it added `web/src/lib/scrollDebug.ts` (439 lines, new) plus
`web/src/lib/__tests__/scrollDebug.test.ts` — there is already
purpose-built scroll instrumentation in the tree. Use it before writing your own.

## Environment rules (non-negotiable)

- **You are running inside a `forge`-hosted session.** Never `pkill forge`,
  `killall forge`, `pkill -f forge`, `pkill -f electron`, `forge env down`, or
  any pattern-matched kill. It terminates this session and every sibling agent.
- Never `kill` a PID you did not personally start and record.
- Air + Vite hot-reload are running. You almost never need to restart anything.
- Ports are dynamically allocated; this worktree's are in `reliant/.dev-ports.sh`
  (FRONTEND_PORT=3071, BACKEND_PORT=8151, GRPC_PORT=9161).
- Report real command output. A confident false "it works" is much worse than
  "this blocked here."
