# Interactive Permissions in Reliant — Research Findings

Branch `engine`, module `github.com/reliant-labs/reliant`. All paths relative to `reliant/`.

## Headline

**There is no interactive tool-permission gate.** No code path in the product ever
asks a human "may this tool run?". The only runtime gate on a tool call is the
static permission-tier comparison in `execute_tools.go`. Everything called
"approval" in this codebase is a *workflow-graph node* (`approval` node type)
that a YAML author places explicitly — it is not derived from what a tool does.

---

## 1. `mode: manual` vs `mode: auto`

`getModeFromInputs` (`internal/workflow/runtime/engine.go:28`) defaults to
`"manual"`. Grepping every read of `mode` in the runtime, it is consumed in
exactly three places:

- `internal/workflow/runtime/workflow.go:2583` — `buildSpawnChildInputs` copies
  `mode` verbatim into spawned child inputs.
- `internal/workflow/runtime/workflow.go:2615-2624` — `resolveParentPermission`
  maps mode → a *static tier*: `"plan"` → `readonly`; `"manual"` and `"auto"` →
  `mutating` (identical). Unknown → unconstrained.
- `internal/workflow/runtime/inline_workflow_executor.go:669` — sub-workflow
  input default.

**`manual` and `auto` are behaviorally identical at runtime.** Both resolve to
`parent_permission = "mutating"`. Neither gates any tool call, per-tool,
per-node, or per-turn. The only mode that changes anything is `plan`, and it
does so by selecting the *static* readonly tier — not by prompting anyone.

The UI confirms the divergence has been accepted: `web/src/store/chatStore.ts:1255`
and `:3380` note "auto_approve and is_planning_mode are now local state only
(removed from backend)".

## 2. The tool-approval path — it does not exist

`Tool.RequiresPermission` is declared on the tool interface
(`internal/llm/tools/tool_wrapper.go:229`) and implemented by ~25 tools
(`write.go:92`, `edit_lines.go:105`, `fetch.go:121`, …). `ToolWrapper`
forwards it at `tool_wrapper.go:282`.

`code_context(symbol: "RequiresPermission", want: "callers")` on the
`ToolWrapper` method returns **only two callers, both tests**
(`file_concurrency_test.go:77`, `response_tool_test.go:285`). Nothing in
production calls it.

This is documented in-tree, deliberately, at
`internal/llm/tools/file_concurrency_test.go:39-50`:

> "The Tool interface's `RequiresPermission` is not an execution-time gate at
> all: outside the per-tool implementations, the only reference in the tree is
> `ToolWrapper.RequiresPermission` passing through to it, and nothing calls that
> during execution."

The only gate that runs before a tool executes is the static tier check at
`internal/workflow/runtime/activities/handlers/execute_tools.go:310-323`:
`MinimumPermissionForTool(toolName)` vs `PermissionAtLeast(grantedPermission, …)`.
Failure returns an error tool-result to the model; no human is involved.

Corroborating: the proto declares `APPROVAL_TYPE_TOOL = 1`
(`proto/reliant/v1/approval.proto:11`), but a repo-wide search for that constant
finds **the proto line and nothing else**. No Go or TS code ever constructs a
tool-type approval. Every `approvals` row written is `APPROVAL_TYPE_WORKFLOW_STEP`.

### What the `approval` node actually does (the machinery that DOES work)

`executeApprovalSignalFlow` (`internal/workflow/runtime/approval_flow.go:114`):

1. Runs the `ApprovalCreate` activity (`approval_flow.go:150`) →
   `ApprovalCreateActivity.Execute`
   (`internal/workflow/runtime/activities/handlers/approval.go:104`), which
   writes an `approvals` row (table created in
   `internal/db/migrations/postgres/20260211000002_init_schema.sql:178`;
   thread_id added `20260811000001_add_approvals_thread_id.sql`).
   Idempotency key is `"<temporal workflow id>:<temporal activity id>"`
   (`approval.go:114`).
2. Blocks on Temporal signal channel `"signal.approval." + approvalID`
   (`approval_flow.go:163`), raced against a timeout timer
   (`approval_flow.go:167-196`).
3. The UI polls `ApprovalService.ListApprovalsByChat`
   (`internal/grpc/services/approval.go:106`); the chat activity SQL views
   surface "awaiting input" from pending `approvals` rows
   (`20260801020000_add_paused_chat_activity.sql:21`).
4. User clicks → `ApprovalService.Approve` / `Deny` / `BatchApprove` /
   `BatchDeny` (`approval.go:136/222/326/424`) → `signalApproval`
   (`approval.go:540`) sends the signal, unblocking the workflow.
   A denial also writes a chat message (`approval.go:556`).

This is the *same* signal-and-DB-row shape as `ask_question`
(`question_flow.go`), just a different channel prefix and table.

## 3. Auto-approve / allowlisting — **nothing exists for tools**

Searched for `auto_approve|autoApprove|allowlist|always_allow|dont_ask|remembered|trusted_command`
across `internal/`, `web/src/`, `proto/`.

**Definitively: there is no persisted approval allowlist at any scope — not per
chat, per project, nor per user.** `auto_approve` was a chat column and it was
*removed*: `proto/reliant/v1/chat.proto:258` `reserved 11; // was: auto_approve
(now workflow param inputs.mode)`, likewise `:392`, `:440`, `:499`. The
replacement pointer (`inputs.mode`) leads to the dead branch in §1. There is no
"don't ask again", no remembered decision, no per-command trust store.

The *only* allowlist in the tree is unrelated to interactive approval: the
**daemon policy** exec allowlist (`internal/daemonpolicy/policy.go:46-52,81-83,177-222`),
a static, non-interactive, config-supplied set of permitted command *basenames*
(`ExecMode` ∈ deny / `allowlist` / `unrestricted`), applied to MCP-grant-scoped
exec (`internal/mcpserver/catalog.go:55`; wire form
`proto/reliant/v1/tools_daemon.proto:446-451`). It is never mutated by a user
approving something. It is a *third* static system, not a memory of interactive
decisions.

There is also a CEL helper `toolRequiresApproval(name)`
(`internal/cel/engine.go:46-50`, `internal/cel/functions.go:13`) available to
workflow edge conditions — a YAML author could route to an `approval` node based
on it. Its tests reference `context.auto_approve`
(`internal/cel/functions_test.go:122`), but no runtime code populates that
variable. This is the one extension point that *could* connect tool identity to
the working approval machinery, and no shipped workflow uses it.

## 4. `unattended: true`

`internal/workflow/runtime/unattended.go`. Propagation is monotone — a child may
turn it on, never off (`propagateUnattended`, `unattended.go:64-70`), mirroring
the `parent_permission` cap.

Because no tool is ever gated on a human (§2), the question "what happens to a
tool that requires permission" has no branch: **the tool just runs**, subject
only to the static tier. Unattended changes nothing about tool execution.

What it *does* affect is the two explicit human-blocking constructs, and the
file is emphatic that **neither fabricates an answer** (`unattended.go:29-32`):

- `ask_user` tool → `executeAskUserInline` returns `unattendedAskUserResponse`
  (`unattended.go:82`): a `[UNATTENDED]` -prefixed result stating the question was
  shown to nobody and nothing was selected on the user's behalf, instructing the
  agent to decide itself and record the decision and reason. No `questions` row.
- `ask_question` node → `executeAskQuestionSignalFlow`, auto-resolved with
  `resolved_by = "unattended"` (`unattended.go:36-38`).

So: **not auto-denied, not auto-approved — auto-resolved-by-agent, with an
audit marker.** For an `approval` node specifically, I found no unattended
short-circuit in `approval_flow.go`; it appears to fall through to the
`defaultApprovalTimeout` path (`approval_flow.go:115-120, 196+`), i.e. an
unattended run hitting an `approval` node *stalls until timeout*. Worth
re-verifying during redesign.

## 5. What the UI shows

`web/src/components/Chat/PermissionsPanel.tsx` + `ApprovalActions.tsx`.

The panel renders only when `usePendingApprovals(chatId)` is non-empty
(`PermissionsPanel.tsx:53`). `ApprovalActions.tsx:32,50` renders exactly two
buttons: **"Approve All"** and **"Deny All"**, wired to `batchApprove` /
`batchDeny` over *every* pending approval id (`PermissionsPanel.tsx:36-50`).

**There is no per-approval UI, no command string, no diff, no tool name.** The
only payload is the `Approval.title`/`description` that the YAML `approval` node
author wrote (`approval.proto:47-48`; `ApprovalCreateInput.Title`,
`handlers/approval.go:45`). A bash command string is never shown, because bash
never raises an approval. The proto carries `tool_name` / `tool_call_id` fields
(`approval.proto:56-57`) that are never populated.

## 6. Interrupt / cancel — this one is real and it does kill processes

`ChatService.InterruptThread` (`internal/grpc/services/chat_interrupt.go:19`,
RPC at `proto/reliant/v1/chat.proto:185`) → `threads.Service.InterruptThread`
(`internal/threads/interrupt.go:108`).

Ordering is deliberate (`interrupt.go:134-140`): read in-flight tool calls,
push per-call daemon cancels (`cancelToolCalls`), **then** signal the workflow
(`signalThreadInterrupt`, `interrupt.go:172`, sending
`v2.InterruptThreadSignalName`). Killing before releasing the workflow prevents
a successor step re-entering a non-idempotent tool.

Daemon side: the gateway pushes `ServerMessage_ToolCancel`, handled at
`internal/toolexec/daemonruntime/runtime.go:778-783` →
`cancelToolExecution(requestId)`, which cancels the `context.CancelFunc` stored
in `cancelByReq` (`runtime.go:93-94`). Children are spawned in their own process
group (`daemonruntime/process_group_unix.go:11` `Setpgid: true`;
`cmd_exec.go:90`), and `internal/toolexec/remote_executor.go:299` notes the
cancel "signals the process group". So **yes — a running shell process is
actually killed.**

Cancelled calls get a durable terminal status and a real tool_result
(`CancelledToolResultContent`, `interrupt.go:~195`): "Tool execution cancelled by
user… its effects are unknown, so verify before re-running it." Temporal history
stays valid because the activity returns normally rather than being cancelled
wholesale (`interrupt.go:90-99`).

In-workflow coordination is `ThreadInterruptCoordinator`
(`internal/workflow/runtime/thread_interrupt_coordinator.go:33,98,125`), which
is epoch-based and per-thread.

Interrupt is the only interactive control that reliably stops a tool, and it is
*post-hoc* — the user stops something already running.

## 7. How the static ladder and interactive approvals interact: **they don't**

They are fully independent systems that never reference each other.

| | Static ladder | Interactive approval |
|---|---|---|
| Trigger | every tool call | only an `approval` node in workflow YAML |
| Input | tool name → tier | author-written title |
| Decider | `PermissionAtLeast` string compare | a human clicking Approve All |
| Location | `execute_tools.go:310-323` | `approval_flow.go:114` |

Concretely:

- **A readonly agent gets no approval prompts** from its readonliness. It gets
  prompts iff its workflow graph contains an `approval` node. Its readonly tier
  simply makes mutating tools return an error string to the model.
- **An orchestrator agent does not "skip" approvals** — it hits exactly the
  `approval` nodes in its graph, same as anyone. Its tier only widens which
  tools clear the static compare.
- The one *near*-interaction is `resolveParentPermission`
  (`workflow.go:2615`): `mode` — the input that *sounds* interactive — feeds the
  *static* ladder. That is the whole connection: an interactive-sounding knob
  wired to a non-interactive gate.

### Implication for redesign

Interactive gating is **not** where the real safety comes from today, because it
never fires on tool execution. Today's actual safety surface is three static,
non-interactive layers:

1. the tool permission tier (`MinimumPermissionForTool` / `PermissionAtLeast`),
2. `parent_permission` monotone capping on spawn (`buildSpawnChildInputs`),
3. daemon `ExecMode`/`ExecAllowlist` on shell exec (`daemonpolicy/policy.go`),

plus one *post-hoc* human control (interrupt, §6).

A redesign therefore **cannot build on interactive gating — it has to build it.**
The working parts to reuse are the approval row + `signal.approval.<id>` +
timeout flow (`approval_flow.go`), which is proven, replay-safe and
loop-iteration-addressable; and the daemon cancel path, which genuinely kills
processes. The parts to design from scratch are: a call site in `execute_tools.go`
that consults tool identity + arguments, a persisted decision store (nothing
exists), and a per-approval UI that shows the actual command (today's UI shows a
title and a single Approve-All button).
