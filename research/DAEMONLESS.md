# Running a workflow with NO DAEMON CONNECTED

Research only. Branch `engine`. All line refs are `reliant/` module paths.

## Verdict

**Nothing blocks *starting* a run without a daemon, and nothing blocks the
workflow engine itself.** The daemon requirement is entirely concentrated in
(a) individual tools that are marked `ToolRunsOnDaemon`, and (b) two node
types (`create_worktree`, `run`) that route through the daemon transport.
A daemon-less agent chat already runs today — the hermetic e2e harness proves
it (`e2e/stories/harness_test.go:222`, `DaemonRouter: nil`).

Minimum change set to make a daemon-less workflow *useful*:

1. Re-mark `fetch` and `websearch` as `ToolRunsAnywhere`
   (`internal/llm/tools/registry.go:467-468`). Genuinely safe — both are pure
   `net/http`, zero filesystem. One-line each.
2. Leave `code_context` on the daemon (it shells out to `gopls`/`node` and
   `os.Open`s real files — `code_context.go:752`, `code_context_engines.go:450`,
   `code_context_source.go:159`). Not safe to move.
3. Author a workflow whose tool set excludes `tag:shell` and `code_context`,
   and which uses no `create_worktree` / `run` nodes.
4. Optional polish: make the daemon-offline circuit breaker not trip for a
   workflow that never targets the daemon (it already only counts
   daemon-targeted failures, so this is likely a no-op).

That is it. No engine change, no schema change, no gate to remove.

---

## Q1 — Is a daemon required to START a run?

**No.** All four `ExecuteWorkflow` call sites go straight from input
construction to `s.tempClient.ExecuteWorkflow` with no daemon resolution,
precondition, or wait:

- `internal/grpc/services/chat_crud.go:280` (CreateChat)
- `internal/grpc/services/chat_send.go:372` (ghost recovery)
- `internal/grpc/services/chat_send.go:1080` (SendMessage)
- `internal/grpc/services/chat_crud.go:1298` (CompactChat)

The only daemon-adjacent step is `injectSessionDaemonID(initialData, chat)`
(chat_send.go:358, :1069), which copies a *pre-existing* session daemon id from
the chat row into workflow inputs. It does not resolve or require one.

**A preflight gate exists but is DEAD CODE.**
`PreflightDaemonCheckActivity` (`internal/workflow/runtime/activities/handlers/preflight_daemon.go:27`)
would hard-fail with *"this workflow requires a daemon but none is available"*
(:100-101) — but `code_context(PreflightDaemonCheckActivity, want: callers)`
returns **no inbound edges**. It is never registered or invoked. Worth deleting
in the same PR series so nobody wires it back up.

`NewChatService`'s daemonRouter arg is nil-tolerant: it is used only for the
greenfield code-presence probe, which self-skips when nil
(harness_test.go:244-246 comment).

## Q2 — The `daemon:` selector when UNSET

`Workflow.daemon` / `Node.daemon` are `CelDaemonSelector`
(workflow_v2.proto:1373, :254). Unset ⇒ the `*DaemonSelector` passed down is
**nil**, which means "default resolution", not "no daemon".

Resolver: `NATSDaemonRouter.resolveDaemonID`
(`internal/toolexec/daemon_router_nats.go:130`). Three steps:

1. Local `ConnectedDaemonResolver` (`daemon_resolver.go:58`) — connected
   daemons on this gateway. **With a nil selector it prefers `Type == "local"`**
   (daemon_router_nats.go:148-153); that is the only "default" in the system.
2. Control plane `ResolveDaemon` RPC (`resolveViaControlPlane`, :278).
3. DB fallback: `ListDaemonsByUserID` + `ListAttachedDaemonIDsForUser`, prefer
   a fresh attachment, else any matching row (:174-213).

If all three miss, resolution **hard-errors, with a two-way split** (:229-247):

- A daemon *record* exists but isn't routable ⇒ `ErrDaemonPending`
  ("your machine is still starting") — retryable, and the frontend's
  `classifyDaemonWait` keys on it.
- No record at all ⇒ flat `"no daemon available: no machine is connected to
  your account yet"`.

There is no graceful degradation to server-side execution at this layer. The
degradation, such as it is, happens above — see Q5.

## Q3 — Which node types hard-require a daemon?

| Node | Needs daemon? | Evidence |
|---|---|---|
| `call_llm` | **No** | `call_llm.go` touches the daemon only to *read* `worktree.DaemonID` for shell-platform description text (:867-882). Purely cosmetic; empty is fine. |
| `execute_tools` | Only per-tool | Routing is per-tool at `remote_executor.go:159-170`. A tool set with no daemon-located tools never reaches `executeOnDaemon`. |
| `compact` | No | Same LLM path as call_llm. |
| `approval`, `ask_question`, `save_message` | No | DB + signal only. |
| `workflow`, `loop`, `join`, `router` | No | Pure control flow in the workflow. |
| `create_worktree` | **Yes, hard** | `worktree.go:87-89` — `if a.daemonRouter == nil { return ... "worktree creation requires a daemon connection" }`. Every repo op is a `SendDaemonCommand` (`worktree.create`, :185). Same for `DeleteWorktreeActivity` at :347-349. |
| `run` | **Yes, effectively** | `RemoteRunExecutor.ExecuteCommand` (`run_executor.go:67`) marshals the command into the **shell tool** and dispatches through `ToolExecutor` (:112). `shell` is `ToolRunsOnDaemon`, so this always lands on a daemon. |

`create_worktree` and `run` are genuinely daemon-bound: one needs a git
checkout on disk, the other needs a shell. These are not removable blockers,
they are the definition of what a daemon is for.

## Q4 — Are fetch / websearch / code_context genuinely daemon-requiring?

The registry's own doc comment at `registry.go:126-128` names network-only
tools like fetch/websearch as the canonical *`ToolRunsAnywhere`* example, yet
:467-468 marks both `ToolRunsOnDaemon`. The stated rationale is a deliberate
policy choice, not a technical need — the comment reads *"routed to daemon so
HTTP requests originate from the user's machine."*

- **`fetch`** — `internal/llm/tools/fetch.go`. Imports: `net/http`, `net/url`,
  `io`, html-to-markdown, goquery, go-readability. **No `os`, no `exec`, no
  `filepath`.** `Execute` (:126) builds one `http.NewRequestWithContext`
  (:152). **Safe to move to `ToolRunsAnywhere`.**
- **`websearch`** — `internal/llm/tools/websearch.go`. Same story: `net/http`
  + goquery, `searchDuckDuckGo` at :213 issues a single GET (:217). **No
  filesystem access at all. Safe to move.**
- **`code_context`** — **NOT safe.** It `os.Open`s source files
  (`code_context_source.go:159`), resolves paths against a checkout root
  (`code_context.go:691-692`), and spawns language servers:
  `exec.CommandContext` at `code_context.go:752` and
  `exec.CommandContext(ctx, "node", serverPath, ...)` at
  `code_context_engines.go:450`, after probing for `node_modules/typescript`
  on disk (:261-262). The registry comment is accurate here: *"Daemon-located:
  it needs the real checkout and a language server."* Leave it.

The one caveat on moving fetch/websearch: egress IP changes from the user's
machine to the server. That is a product/privacy decision, not a correctness
one — worth naming in the PR, but it does not make the change unsafe.

## Q5 — What a daemon-less failure looks like TODAY

**A daemon-less run does not crash — it degrades, then self-pauses.**

Failures arrive as **non-error activity results** whose `ToolResult` is
error-shaped and carries the stable substring `"no daemon connected"`
(`internal/daemonoffline/`, `ErrorSubstring`). Go-level activity errors are
kept deliberately neutral so the breaker keys on the result, not the error.
Detection is narrow: the substring must be present AND any wrapped
`connect.Error` must be `CodeUnavailable`.

**There is a circuit breaker.** `DaemonOfflineCircuitBreaker`
(`internal/workflow/runtime/daemon_offline_tracker.go`), threshold **3**
consecutive daemon-targeted failures. On trip it pauses the workflow and emits
*"Paused: no machine is connected. Start your machine and send a message to
continue."* One breaker per execution, shared with every StepExecutor via the
PauseController — which is what makes it work for agent loops, whose
per-iteration `ExecuteTools` completions never surface as main-loop step
events. It rides the workflow's deterministic stack and is never persisted.

`e2e/stories/story_05_daemon_offline_breaker_test.go` asserts the whole loop:
3 offline tool turns → workflow `Paused` → the pause notice appears in
`GetChatUpdates` as a `CHAT_UPDATE_TYPE_ERROR` → user sends a message →
`PauseService.ResumeWorkflow` → 4th LLM turn runs → `Completed`, chat `Idle`.
Exactly 3 LLM turns are burned; bounding that is the breaker's whole point.

**What already works with no daemon**, per `harness_test.go` (`DaemonRouter:
nil`, comment at :222 "hermetic: no daemon transport; worktree ops
unavailable"): the full Temporal worker, all activities, `ChatService`,
`QuestionService`, `PauseService`, LLM turns, tool execution via a
server-side executor, message persistence, chat updates. Every story test in
that suite is already a daemon-less workflow run.

So we are close. The gap is not the engine; it is the *tool menu*.

## Q6 — Project / working_directory coupling

`remote_executor.go:143-152` rejects an empty `ProjectID` with `"Missing
project ID in tool request"` / `INVALID_REQUEST`. Note this is a **flat
validation on every tool**, applied before the location switch at :159 — so a
purely server-side tool still needs a `ProjectID`.

But `ProjectID` is an **identifier, not a path**. `executeOnServer` (:174+)
folds it into a context map as `project: {id, path, name}`, plus optional
`worktree: {id, path}` and `repos`. Server-side tools (plans, tasks, spawn
observability) use the id as a **DB scoping key** — they never touch
`project.path`. That is why the hermetic harness works: it creates a real
`db.Project` row with a `projectPath` and a main `Worktree`
(harness_test.go:180-200), and every server-side tool is satisfied by the row
alone.

So `project` is fully resolvable to something non-filesystem. Only the
daemon-located tools and the `run` / `create_worktree` nodes dereference
`ProjectPath` / `WorktreePath` into an actual directory
(`run_executor.go:85-92` picks `WorktreePath` else `ProjectPath` as the shell's
cwd). The `ProjectID` check at :143 is not a daemon blocker — it is a
scoping requirement satisfiable by a DB row.
