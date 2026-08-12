# Spec: Asynchronous `spawn` + sub-agent message passing

Status: proposal
Author: drafted from a code review of `main` @ `a5942dba`
Scope: `spawn` tool semantics, a new `spawn_*` tool family, an agent mailbox, and the agent-loop lifetime rule that makes all of it safe.

---

## 1. What this changes, in one paragraph

Today `spawn` blocks: the parent's `execute_tools` step does not settle until every spawned sub-agent has finished, and the parent gets back one string — the child's last assistant message. This spec makes `spawn` return a handle immediately, adds `spawn_wait` / `spawn_list` / `spawn_output` / `spawn_cancel` to observe and control live sub-agents, and adds `spawn_send` to pass messages into a running sub-agent. Delivery is via a durable mailbox drained at a safe step boundary, and every queued item is wrapped in a `<system>` note so the receiving model knows the message arrived out-of-band rather than as part of its original brief. The load-bearing constraint — **a sub-agent only survives while its parent's agent loop is still turning** — is handled by a new general-purpose `wait` node that blocks on a CEL condition over live runtime state; "are any agents still pending" is one condition among several, not a bespoke node.

---

## 2. How it works today

### 2.1 `spawn` is a virtual tool

`spawn` is not in the tools registry. It is a schema-only stub synthesized per-turn and intercepted by the workflow runtime.

| Stage | Location |
|---|---|
| Schema + description synthesized | `internal/workflow/runtime/activities/handlers/call_llm.go:1192-1277` |
| Gated off past depth 1 | `call_llm.go:274-276` — `const maxSpawnDepth = 1` |
| Preset list attached to the call | `call_llm.go:885-893` |
| Preset validated | `internal/workflow/runtime/activities/handlers/execute_tools.go:327-353` |
| Intercepted (split from regular tools) | `internal/workflow/runtime/workflow.go:2167-2181` (`splitProtoToolCalls`) |
| Parsed | `workflow.go:2260-2332` (`parseSpawnToolCall`) |
| Executed | `workflow.go:2373` (`executeSpawnInline`) |
| Result fetched | `workflow.go:2728-2775` (`fetchSpawnResult`) → `FetchThreadResult` activity |
| Fan-out / barrier | `workflow.go:2781-2923` (`executeToolsWithSpawnSupport`) |
| Filter syntax `spawn:builtin://agent(a,b)` | `internal/llm/tools/registry.go:151-250` |
| Permission floor (orchestrator-only) | `internal/llm/tools/permissions.go:79` |

The tool description currently instructs the model, verbatim (`call_llm.go:1239`):

> **PARALLELISM — spawns are SYNCHRONOUS, not async:** a single spawn call BLOCKS until that sub-workflow finishes and returns. To run sub-agents IN PARALLEL you MUST emit MULTIPLE spawn calls in a SINGLE assistant turn…

### 2.2 A sub-agent has no Temporal identity

This is the single most important fact in the codebase for this design. `executeSpawnInline` does **not** start a Temporal child workflow. It constructs an `InlineWorkflowExecutor` (`internal/workflow/runtime/inline_workflow_executor.go:71-83`) and calls `.Execute()` on the parent's own workflow goroutine (`workflow.go:2603-2638`).

`workflow.go:1914-1920` records why, and what it cost to learn:

> A spawn is NOT a child Temporal workflow … the previous implementation's `TerminateWorkflow(child_workflow_id)` always failed with "workflow not found for ID": that id names a thread and a DB row, not a Temporal execution.

Consequences that constrain everything below:

- **You cannot signal a sub-agent.** Signals must target the parent execution and be routed by `thread` / `tool_call_id` — the shape `cancel_thread` already uses (`workflow.go:1901-1950`), and the shape `signal.question.<id>` already uses (its `temporal_workflow_id` is the parent's, `question_flow.go:64`).
- **A sub-agent lives and dies with its parent's execution.** When the parent workflow function returns, detached goroutines are abandoned.

### 2.3 The fan-out is concurrent but has a hard barrier

`executeToolsWithSpawnSupport` (`workflow.go:2781`):

1. Regular tools dispatched as one `ExecuteTools` activity (`:2823-2841`).
2. Fast path returns directly when there are no spawns (`:2844-2846`).
3. One spawn → called inline, blocking (`:2890`). N spawns → one `workflow.Go` each plus a channel (`:2894-2901`), then a **blocking collection loop** `for range spawnConfigs { resultCh.Receive(...) }` (`:2902-2906`).
4. `resultSettable.SetValue(...)` (`:2922`).

So N spawns genuinely run in parallel, but the combined future settles only when **every** member reports. The parent's step loop parks on that single future via `waitForAnyCompletion` (`workflow.go:1564`, selector at `:3244-3273`). One slow child stalls all results.

### 2.4 The result is thin

`FetchThreadResult` (`internal/workflow/runtime/activities/handlers/fetch_thread_result.go:60-168`) returns the concatenated TEXT blocks of the child thread's **last assistant message**, prefixed at `workflow.go:2767-2769`:

```go
prefixedContent := fmt.Sprintf("<system>Use agent_id: %s for future resumption</system>\n\n%s", childThread, content)
```

Note this `<system>` convention already exists — the wrapper this spec proposes is consistent with it, not new.

No token accounting, no turn count, no artifact list, no structured verdict.

### 2.5 The agent loop terminates on exactly one condition

`internal/workflow/builtin/agent.yaml:122-136` is the whole workflow — a single `loop` node:

```yaml
- id: agent_loop
  type: loop
  while: (outputs.tool_calls != null && size(outputs.tool_calls) > 0) || outputs.has_feedback == true
```

Evaluated after each iteration at `internal/workflow/runtime/loop_executor.go:492-511`. **The only terminal condition is "the LLM emitted no tool calls."** There is no max-iteration cap in production (`simulator.go:100` is simulator-only). `has_feedback` comes from `ask_question` (`inline_workflow_executor.go:1194-1231`) and is the existing precedent for *external input re-entering a finished turn*.

### 2.6 There is no message-injection mechanism, and no mailbox

- `InjectMessageConfig` (`internal/workflow/runtime/child_workflow_init.go:44-49`) is **create-time only**. All call sites are child-creation sites; the write happens via the `SaveMessage` activity at `child_workflow_init.go:146`.
- A user's follow-up into a *running* chat is a bare `INSERT` into `messages` with **no signal at all** (`internal/grpc/services/chat_send.go:676-693`). It works only because `call_llm` re-reads live DB history every iteration (`call_llm.go:237-239` → `db_helpers.go:71-86`).
- **The only queue-shaped tables are `questions` and `approvals`.** There is no mailbox table of any kind.

The gap this leaves: if the recipient's loop has already exited, an inserted message is never read. Nothing re-enters a finished loop.

### 2.7 Durable state a `spawn_list` / `spawn_wait` can already query

| Table | Key columns | Anchor |
|---|---|---|
| `tool_calls` | `id`, `tool_name='spawn'`, `thread_id` (the **parent's** thread), `status`, `child_workflow_id`, `input` | migration `20260801010000_add_tool_calls.sql:29-50` |
| `workflows` | `id`, `parent_id`, `thread`, `status`, `outcome` | `internal/db/postgres/generated/models.go:556-570` |
| `threads` | `id`, `parent_thread_id`, `origin='spawn'`, `status`, `completed_at` | `models.go:492-504`; origin added `20260729000000` |

`tool_calls.status`: 1 pending, 2 executing, 3 completed, 4 failed, 5 cancelled, **6 backgrounded** (`internal/db/core/tool_call.go:18-36`). Written from workflow code only via the `EmitToolCallStatus` activity (`handlers/emit_tool_status.go:62-127`), called by `notifyToolCallStatus` (`workflow.go:3494-3528`).

`internal/db/thread_activity.go:11-20` states the progress rule that matters here:

> Messages are the only durable per-thread progress evidence the schema has: step executions and position checkpoints are written for the ROOT run only, so a spawned agent thread has neither, while every turn it takes lands a message.

### 2.8 Two pieces of dead code worth knowing about

- **`PendingToolResultGroup`** — `internal/workflow/runtime/pending_tool_results.go`, entire file, **zero references repo-wide** (verified). It is a group that owns a Future, counts `ExpectedCount` members, accumulates results incrementally, and settles when complete. It is the *synchronous barrier* helper; useful for the `wait: true` path below, but it is not an async seam.
- **The `agent` tool** — `internal/llm/tools/factory.go:321-339` returns a schema-only stub whose comment says "execution should be intercepted by workflow before reaching here." `splitProtoToolCalls` only intercepts `"spawn"` and `"ask_user"`, so an `agent` call would fall through to `regularToolCalls` and hit the stub's error. It is a trap. `names.go:49` (`ToolAgent`) and `permissions.go:79` reference it.

---

## 3. Why change it

1. **The parent's turn is dead time.** An orchestrator that delegates cannot read a file, prepare the next brief, or update the plan while its agents work.
2. **The barrier wastes the parallelism it enables.** Five spawns where one takes 10× the others means four idle agents' results sit undelivered.
3. **Fan-out must be pre-planned.** Because parallelism requires batching into a single assistant turn, work *discovered* during a run cannot join an in-flight fan-out. The tool description has to teach this workaround explicitly — a strong smell that the mechanism is wrong.
4. **There is no way to steer a running agent.** No progress check, no redirect, no "stop, requirements changed." The only control is cancel-and-lose-everything.
5. **`spawn` cannot express a long-running agent at all.** Anything expected to outlive the parent's patience simply cannot be modelled.

---

## 4. Tool surface

### 4.1 What Claude Code has in this space, and how it maps

I exercised these during this review — including sending a message to a live sub-agent mid-run.

| Claude Code | Behavior | Reliant equivalent proposed here |
|---|---|---|
| `Agent(prompt, run_in_background)` | Async by default; returns an `agentId` immediately | `spawn(..., wait=false)` |
| `ListAgents` | Lists spawned subagents + peer sessions with status | `spawn_list` |
| `SendMessage(to, message, summary)` | Enqueues; **"Message queued for delivery at its next tool round"** | `spawn_send` |
| `TaskOutput` | Reads a background task's output | `spawn_output` |
| `TaskStop` | Kills a background task | `spawn_cancel` |
| *(auto `<task-notification>`)* | Completion is pushed to the parent, unprompted | reaper → `<system>` injection |
| `Monitor` / `bash_wait`-style | Block until a condition | `spawn_wait` |

Two observations from actually using them that shaped this design:

- **The reply to `SendMessage` is the honest one:** *queued for delivery at its next tool round.* It does not claim the message was read. The sender's tool result must say the same, or the model will assume delivery and act on a false premise.
- **I have no "wait for agent" tool** — because completion auto-notifies. That is the right default, but Reliant still wants `spawn_wait` for the case "I genuinely cannot proceed without agent X," so the model isn't forced to invent filler work to stay in the loop.

### 4.2 Naming

Use the `spawn_*` family, matching the repo's own `bash` / `bash_list` / `bash_output` / `bash_wait` / `bash_kill` precedent (`internal/llm/tools/bash_*.go`). `spawn` is already the established noun throughout: `threads.origin='spawn'`, `tool_calls.tool_name='spawn'`, `SpawnToolRenderer.tsx`.

**Delete the dead `agent` tool** rather than leaving two names for one concept — `factory.go:321-339`, `names.go:49`, and the `ToolAgent` arm of `permissions.go:79`. Per house rule, removal means zero residual references or explanatory comments; git history is the record.

### 4.3 The tools

#### `spawn` (modified)

```
preset     string   required
prompt     string   required
title      string   optional
agent_id   string   optional — resume an existing sub-agent
wait       bool     optional — default TRUE in phase 1, flipped to FALSE in phase 4 (§8)
```

- `wait=true` → today's behavior exactly, byte for byte.
- `wait=false` → returns immediately:

```
Spawned "reviewer" as agent_id: 7f3a…  (status: running)

<system>
This agent is running in the background. Its result is NOT in this tool result.
You will be notified when it finishes. Continue with other work; call spawn_wait
if you cannot proceed without it, or spawn_send to give it new instructions.
</system>
```

`wait` mirrors `bash(run_in_background=...)` — the same shape already exists in this codebase, so the model handles it without a new concept.

#### `spawn_wait`

Modeled directly on `bash_wait.go`, including its hard-won semantics.

```
agent_ids        []string  optional — default: all live agents of this thread
mode             enum      "any" (default) | "all"
timeout_seconds  int       default 240, max 240
```

Non-negotiable properties inherited from `bash_wait.go:39-99`:

- The tool-execution context cancels every tool call at 5 minutes, so the budget ceiling is **240s** with a clamp, not a rejection (`bash_wait.go:129-138`).
- **A timeout is a successful result, not an error** (`bash_wait.go:226-243`). It returns `timed_out: true`, kills nothing, and tells the model to call again. Returning an error here trains the model to treat a slow agent as a failed one.
- Verify the agent exists *before* committing to a long block (`bash_wait.go:148-158`).

#### `spawn_list`

Read-only. Query is `tool_calls WHERE tool_name='spawn' AND thread_id = <caller's thread>` joined to `workflows` and `threads`. `tool_calls.thread_id` is the **parent's** thread, so this needs no new column.

Returns per agent: `agent_id`, title, preset, status, elapsed, last-activity time (`MAX(messages.created_at)`, per §2.7), turn count, and whether it is gated on a question/approval.

#### `spawn_output`

Peek at a running agent without waiting — the `bash_output` analogue. Returns the last N assistant messages / tool calls from the child thread. This is what makes "is it stuck or just slow?" answerable without cancelling.

#### `spawn_send`

```
agent_id  string  required
message   string  required
```

Enqueues into the mailbox (§5). Returns the honest receipt:

```
Queued for delivery to "reviewer" (7f3a…). It will be read at that agent's next
turn. It has NOT been read yet — do not assume it has acted on this.
```

If the target agent's loop has already exited, this **fails** with guidance to use `spawn(agent_id=…)` instead. Rationale in §7.4.

#### `spawn_cancel`

Wraps the existing `cancel_thread` signal (`workflow.go:1901-1950`), which already accepts either `thread` or `tool_call_id` and is already observed at the child's step boundary (`inline_workflow_executor.go:656-666`). This is mostly a tool-surface wrapper over working machinery.

### 4.4 Tool gating

`maxSpawnDepth = 1` (`call_llm.go:275`) means sub-agents never receive `spawn`. But a sub-agent **does** need to talk back. Gating:

| Tool | Depth 0 (orchestrator) | Depth 1 (sub-agent) |
|---|---|---|
| `spawn`, `spawn_wait`, `spawn_list`, `spawn_output`, `spawn_cancel` | yes | no |
| `spawn_send` | yes (→ children) | yes (→ parent only) |

v1 is parent↔child only. Sibling messaging is a natural extension and the schema below permits it, but it introduces a coordination surface (discovery, addressing, permission) that should not ride along with this change.

---

## 5. Message passing

### 5.1 The mailbox

New table. This is the first queue-shaped table besides `questions` / `approvals`, and it deliberately copies their proven create → wake → resolve shape.

```sql
CREATE TABLE agent_messages (
    id             text PRIMARY KEY,
    chat_id        text NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    from_thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    to_thread_id   text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    kind           integer NOT NULL,   -- 1=message, 2=completion, 3=cancelled, 4=failed
    body           text NOT NULL,
    tool_call_id   text,               -- the spawn call that owns the subject agent
    status         integer NOT NULL,   -- 1=queued, 2=delivered
    created_at     timestamptz NOT NULL,
    delivered_at   timestamptz,
    delivered_message_id text REFERENCES messages(id) ON DELETE SET NULL,
    CONSTRAINT agent_messages_delivered_has_time
        CHECK (status <> 2 OR delivered_at IS NOT NULL)
);

CREATE INDEX idx_agent_messages_inbox
    ON agent_messages(to_thread_id, created_at) WHERE status = 1;
```

The `CHECK` follows the same principle as `tool_calls_completed_has_completed_at` (`20260801010000:45-49`): a row claiming delivery without a delivery time is a contradiction the schema should refuse to store.

### 5.2 Why a queue and not a bare INSERT into `messages`

A bare insert is what `chat_send.go:676-693` does for users, and it is tempting because `call_llm` reloads history every turn. It is wrong here, for a specific and serious reason.

The product has a hard invariant, stated in the `tool_calls` migration (`20260801010000:14-21`): *an assistant message with `tool_use` blocks must always reach the LLM with matching `tool_result` blocks, or the provider deadlocks the conversation.* An agent is mid-turn most of the time. Inserting a user message between an assistant-with-tool_calls row and its tool_results row produces exactly that deadlock. `call_llm.go:198-210` already warns about the adjacent symptom.

So: queue in `agent_messages`, drain only at a boundary where the history is known-consistent.

### 5.3 Delivery

**Drain point:** the top of each agent-loop iteration, immediately before `call_llm`. At that point `execute_tools`' `save_message` has already written tool results, so history is consistent. This is the same boundary where `DoCheckPause` and `IsCancelled` already sit (`inline_workflow_executor.go:640-666`).

**Mechanism:** a `DrainAgentMessages` activity collapses all queued rows for the thread into **one** user-role message, marks them delivered, and records `delivered_message_id`.

**Format** — the `<system>` wrapper the brief asks for:

```
<system>
2 messages were queued while you were working and are delivered here, in order.
They arrived AFTER your last action — treat them as new instructions that may
supersede your current plan, not as context you have already accounted for.
</system>

<message from="orchestrator" queued_at="2026-08-10T14:03:11Z">
Drop the auth work. The migration is the blocker — do that first.
</message>

<message from="orchestrator" queued_at="2026-08-10T14:05:02Z">
Also: the schema changed on main, re-read it before you start.
</message>
```

Completion notifications to the parent use the same envelope with `kind=2`:

```
<system>
Sub-agent "reviewer" (agent_id: 7f3a…) finished while you were working.
</system>

<agent_result agent_id="7f3a…" preset="code_reviewer" status="completed" turns="14">
…the child's final assistant text…
</agent_result>
```

### 5.4 Waking a blocked recipient

If the recipient is parked in a `wait` node or in `spawn_wait`, a mailbox insert must wake it. Reuse the established pattern exactly: signal name `signal.agent_message.<to_thread_id>`, sent to the **parent's** `temporal_workflow_id` (sub-agents have no Temporal identity, §2.2). This is precisely how `signal.question.<id>` already reaches an inline spawn (`question_flow.go:64`, `internal/grpc/services/question.go:179-180`).

This signal is the mailbox provider's wake source in §6.3 — the `wait` node does not know what a message is, only that a provider it registered woke it and its condition should be re-evaluated.

For the common in-process case — orchestrator messages its own child, same Temporal execution — the wake is an in-memory `workflow.Channel` send, no signal needed. The signal path covers the out-of-process sender (a gRPC caller, the UI).

---

## 6. Keeping the loop alive — the core problem

### 6.1 The failure mode

When the parent's workflow function returns, every detached `workflow.Go` goroutine is abandoned. If the LLM emits no tool calls while three agents are running, `while` evaluates false (`agent.yaml:128`), the loop exits, and three agents die mid-flight — leaving `threads.status='running'` rows with no execution behind them, the exact "ghost" shape `chat_send.go:495-506` already has to cope with.

So async spawn is **not** just "don't await the future." It requires a loop-lifetime rule.

### 6.2 Rejected: `|| pending_agents > 0` in the `while`

The obvious fix is a third disjunct on `agent.yaml:128`. It is wrong on its own: the loop would immediately re-enter `call_llm` with nothing new in the thread, and the model would burn a full turn — tokens, latency, and a junk assistant message — saying "still waiting." Repeat until the child finishes. A `while` clause can keep a loop alive; it cannot make it *block*.

### 6.3 Chosen: a general-purpose `wait` node with a CEL condition

Rather than a bespoke `await_agents`, add one node type that **blocks until a CEL condition over live runtime state becomes true**. "Any agents still pending" is then a condition, not a node type.

```yaml
- id: wait_for_agents
  type: wait
  args:
    until: "runtime.agents.completed > 0 || runtime.agents.pending == 0"
    timeout: 30m      # required — see §6.6
    poll: 5s          # optional; omit to wake only on registered events
```

Outputs: `satisfied` (bool), `timed_out` (bool), `waited_ms` (int), and `runtime` — the state snapshot the node exited on, so downstream CEL can branch on *why* it woke.

#### The `runtime` namespace

The condition evaluates against a namespace assembled from **wait providers**. A provider contributes two things: a slice of state, and a wake source.

| Provider | Namespace | Wake source |
|---|---|---|
| spawn registry | `runtime.agents.{pending, completed, running[]}` | in-workflow completion channel |
| mailbox | `runtime.messages.{pending}` | `signal.agent_message.<thread>` (§5.4) |
| background processes | `runtime.processes.{running, by_id[]}` | daemon status poll |

The first is what this spec needs. The other two are named because they decide whether the generalization is worth paying for — see §6.5.

#### Evaluation model

The node runs a Temporal selector over every registered provider's wake channel plus, optionally, a `poll` timer and the `timeout` timer. On each wake it rebuilds the namespace and re-evaluates `until`. This is the same server-side-poll economics `bash_wait` already documents (`bash_wait.go:52-56`): the loop costs timer fires, **not model round-trips**.

**Hard constraint: the condition must evaluate over workflow-resident state only.** Providers *push* their state into workflow memory — via in-process channel or signal — and the node reads that. A provider that issues a DB query per evaluation would record an activity in Temporal history on every tick and grow history without bound. This is the one rule that makes the node safe, and it must be enforced at the provider interface, not by convention.

#### Wiring in `agent.yaml`

```yaml
    outputs:
      tool_calls:   "{{nodes.call_llm.tool_calls}}"
      has_feedback: >
        {{(has(nodes.ask_question) && has(nodes.ask_question.has_feedback) ? nodes.ask_question.has_feedback : false)
          || (has(nodes.wait_for_agents) && nodes.wait_for_agents.runtime.agents.completed > 0)}}

    nodes:
      - id: wait_for_agents
        type: wait
        args:
          until: "runtime.agents.completed > 0 || runtime.agents.pending == 0"
          timeout: 30m

    edges:
      - from: call_llm
        cases:
          - to: wait_for_agents
            condition: nodes.call_llm.tool_calls == null || size(nodes.call_llm.tool_calls) == 0
            label: "wait_for_agents"
```

Two things to note about that `until`:

- It is **not** "wait for all agents." It proceeds as soon as there is *something to deliver* (`completed > 0`), so the model reacts to the first finisher instead of the slowest. The `|| pending == 0` arm is the exit for "nothing left to wait on," which is what lets the loop actually terminate.
- `has_feedback` keys off `completed > 0` specifically, not off `satisfied`. Waking because `pending == 0` with nothing to deliver must **not** re-enter the loop, or it never terminates.

Because this reuses `has_feedback`, **`while` on line 128 is unchanged**. A sub-agent result re-enters the loop by exactly the mechanism a human answer already does.

Ordering with the existing `ask_question` edge (`agent.yaml:209-213`, gated on `inputs.ask`) needs settling: the wait should run **first** — reaping finished agents may give the model more to do, which makes asking the user premature.

### 6.4 The wait node does not deliver

Blocking and delivery are separate concerns, and keeping them separate is what lets the node stay generic. The `wait` node only blocks. Completions and messages are written into the thread by the step-boundary drain (§5.3), which already has to exist for the case where a message arrives while the agent is mid-turn. The node never learns what an agent *is*.

### 6.5 Does the generalization earn itself?

The house test is what a mechanism buys versus costs, and a generalization with one consumer is just indirection with extra steps. This one has three, and two of them already exist in some form:

1. **Pending agents** — this spec.
2. **Background processes.** `bash_wait` (`internal/llm/tools/bash_wait.go`) is this exact node implemented at the tool layer: block server-side, poll, return "still running" without erroring. A graph that starts a build and needs to wait for it currently has no way to express that; `wait until: runtime.processes.by_id['x'].exited` gives it one, and the tool and the node share a provider.
3. **Idle agents awaiting work.** `wait until: runtime.messages.pending > 0` is the primitive for a long-lived worker agent that parks until its orchestrator sends it something — which the mailbox in §5 makes possible for the first time.

The cost is one node type, a provider interface, and a new CEL namespace. Against three consumers and a capability the graph language currently cannot express at all, that is worth paying. If only consumer 1 had existed, the bespoke node would have been the right answer.

### 6.6 Footguns this introduces, and the guards

A CEL predicate that can hang a workflow is a sharper tool than a fixed node. Three guards are not optional:

- **`timeout` is required, not defaulted.** `wait until: false` with no timeout parks a run forever. Parse-time validation should reject a `wait` node with no `timeout`, the same way tool names are validated up front in `names.go`.
- **The namespace is validated at parse time.** `runtime.agents.pendign` silently evaluates false and hangs until timeout — a typo becomes a 30-minute stall with no error. Providers must declare their namespace schema, and unknown paths must fail workflow validation, not evaluation.
- **A timeout is a result, not a failure.** Same rule as `bash_wait.go:226-243`: `timed_out: true` is a normal output the graph can branch on. It must not throw, or a slow build becomes a failed run.

### 6.7 Belt and braces

The `wait` node handles the normal exit. It does not handle an *abnormal* one — an iteration error propagates out of `loop_executor.go:472-484` and the loop never reaches the node.

Required, in addition:

- **Terminal drain**: cancel live agents and write their tool-call statuses on the workflow's abnormal-termination path, next to the existing `Cleanup` logic (`handlers/cleanup.go`).
- **Reconciler sweep**: see §7.3 — this is not optional, because async spawn silently disables the existing repair.

---

## 7. Consequences that are easy to miss

### 7.1 Async spawn breaks the existing stranded-spawn reconciler

`ListStrandedSpawnToolCalls` (`internal/db/postgres/queries/tool_calls.sql:55-85`) finds abandoned spawns with:

```sql
WHERE tc.tool_name = 'spawn'
  AND tc.status IN (1, 2)
  AND w.status IN (3, 4, 5, 7)
  AND NOT EXISTS (SELECT 1 FROM tool_call_results r WHERE r.tool_call_id = tc.id)
```

Under async, the spawn tool call receives its result (the handle) **at dispatch**. So `NOT EXISTS (tool_call_results)` stops matching, and the repair at `reconciler.go:1477-1520` silently stops firing for exactly the runs that need it most. The query would keep returning zero rows and look healthy.

The durable anchor must move to the mailbox. New sweep: for each `workflows` row whose thread has `origin='spawn'` and terminal `status`, where no `agent_messages` row of `kind=2` exists for the parent thread → enqueue one. Same fail-closed discipline as the original: never fabricate a completion for a child still running (2) or paused (6).

`tool_calls.status` for an async spawn should be **6 (backgrounded)** — the value already exists (`core/tool_call.go:18-36`) and already means "a result was returned to the LLM while the work continues," which is precisely the situation. `ConvertToBackground` (`internal/grpc/services/tool_call.go:212+`) is the existing precedent.

### 7.2 Every builtin workflow assumes synchronous spawn

`forge-one-shot.yaml`, `gsd.yaml`, `one-ring.yaml`, `auditing-agent.yaml` and the rest orchestrate on the assumption that a spawn's result is in its tool result. Flipping the default without migrating them changes their behavior silently — the orchestrator proceeds with a handle where it expects findings. This is the main reason for the phased default in §8.

### 7.3 The UI reads status from the wrong place

`SpawnToolRenderer.tsx` and `ToolExecution.tsx:128` render spawn progress from the **tool call's** status. Under async the tool call completes in milliseconds, so the UI would show a finished spawn next to a running agent. The swim lane must key off `threads.status` / `workflows.status` instead. Affected: `SpawnToolRenderer.tsx`, `ToolExecution.tsx:128`, `ToolExecutionGroup.tsx:134`, `ActivityIndicator.tsx:194`, `InterleavedTimeline.tsx:165`, `ExecutionSidebar/transformApiData.ts`.

### 7.4 Messaging an agent that already finished

Rejected: silently resurrecting it. A finished sub-agent's loop has exited; there is nothing to deliver into, and reviving it implicitly would make "did my message land?" unanswerable. `spawn_send` fails with a pointer to `spawn(agent_id=…)`, which already resumes a thread (`workflow.go:2307-2317`) and is the explicit, visible way to do it.

### 7.5 Backpressure disappears

Blocking spawn is self-limiting: you cannot start the sixth agent until the fifth returns. Async removes that. A cap is needed — `max_concurrent_spawns` per thread, default ~8, with `spawn` returning a normal (non-error) "at capacity, N running, wait or cancel one" result rather than failing.

### 7.6 Smaller ones

- **Worktrees**: concurrent sub-agents mutating one worktree is already possible, but async makes overlapping windows the norm rather than the exception. Worth deciding explicitly whether async spawns get isolated worktrees.
- **`approvals` has no `thread_id`** (`workflow_ps.go:482-486`), so an approval raised inside a sub-agent cannot be attributed to it. With several agents live at once, `spawn_list` cannot say which one is gated. Adding `thread_id` to `approvals` is a small, separable fix worth doing alongside.
- **`GetPendingQuestionByChatID` is chat-scoped** and returns the newest question from *any* thread (`list_questions_by_chat.go:74-84`). Same problem, same fix direction.

---

## 8. Phasing

Each phase is independently shippable and leaves the tree working.

**Phase 1 — observability, zero behavior change.** Add `spawn_list` and `spawn_output` against existing schema. Add `thread_id` to `approvals`. No spawn semantics change. Immediately useful, and it validates that the DB really can answer "what is running" before anything depends on it.

**Phase 2 — the mailbox.** `agent_messages` table + `spawn_send` + `DrainAgentMessages` at the loop's step boundary + the `<system>` envelope. Still synchronous spawn; the value is steering a *sibling* fan-out already running under a batched turn.

**Phase 3 — async plumbing, opt-in.** `spawn`'s `wait` parameter defaulting to **true**. The generic `wait` node + provider interface + parse-time validation (§6.3, §6.6). Detached spawn registry on `ChildWorkflowTracker`, exposed as the first provider. Reconciler sweep (§7.1). Terminal drain. UI status re-keying (§7.3). Behavior unchanged unless a caller passes `wait=false`.

> Naming collision to settle before this phase: `spawn`'s `wait` **parameter** and the `wait` **node** are different things at different layers. They coexist fine in the code, but the docs and tool description must not blur them — consider `spawn(background=true)` instead, which also reads closer to `bash(run_in_background=…)`.

**Phase 4 — flip the default.** Migrate builtin workflows and presets, rewrite the tool description (`call_llm.go:1237-1248`) to teach async + `spawn_wait` instead of turn-batching, flip `wait` to default false, add the concurrency cap. Delete the dead `agent` tool and `PendingToolResultGroup` if still unused.

---

## 9. Alternatives considered

**Real Temporal child workflows.** Would give each sub-agent an identity, making signals and cancellation native. **Rejected**: it reverts a deliberate migration whose reasons are recorded at `workflow.go:2367-2372` — orphaned children on worker restart, pause/resume no longer applying to spawns, per-workflow task-queue problems. Those were real production failures; async is not worth re-inheriting them.

**Detach to an abandoned top-level workflow** (`PARENT_CLOSE_POLICY_ABANDON`). Removes the loop-lifetime problem entirely. **Rejected**: it also removes chat-wide pause/resume and cancellation, and puts the sub-agent outside every existing reconciliation scope. It trades a tractable problem for an intractable one.

**Poll-only, no auto-injection** — parent must call `spawn_wait`/`spawn_list` to learn anything. **Rejected**: the parent that forgets to poll ends its turn and kills its children. Auto-injection is what makes the lifetime rule enforceable rather than advisory.

**Bare `INSERT` into `messages` for delivery** (what `chat_send.go` does for users). **Rejected**: deadlocks the provider when it lands mid-turn — §5.2.

**Two tools (`spawn` and `spawn_async`)** instead of a `wait` flag. **Rejected**: `bash(run_in_background=…)` is the same shape and already works well in this codebase; two names for one action is worse for both the model and the UI.

**A bespoke `await_agents` node** instead of the generic `wait`. **Rejected** on the buys-vs-costs test in §6.5: three consumers exist, one of them (`bash_wait`) is already implemented at the tool layer because the graph cannot express waiting at all. A single-purpose node would have to be joined by `await_process` and `await_message` within a release.

**Hiding the wait inside the loop executor**, with no YAML surface. Cheapest to build. **Rejected**: it makes a blocking point invisible to anyone reading `agent.yaml`, and "reading the code must tell you what runs" is the house rule. A graph that silently blocks is exactly the kind of magic that costs a day of debugging later.

### Devil's advocate on the chosen design

- **The `wait` node introduces the first mutable CEL evaluation context in the codebase.** Every other CEL site evaluates once over immutable state; this one re-evaluates the same expression against changing values inside a single node execution. That is a genuinely new concept, and it is where replay bugs will come from if the workflow-resident-state rule in §6.3 is ever relaxed. The rule is the whole safety argument — if it turns out providers *need* to query the DB, this design should be revisited rather than patched.
- **A CEL predicate can hang a run in ways a fixed node cannot.** §6.6 lists three guards. They are stated as required, but required-by-prose is not required-by-compiler; the parse-time validation is the part most likely to get deferred and most likely to be regretted.
- **Reusing `has_feedback` is slightly dishonest naming.** A sub-agent result is not "feedback." The upside is that `while` on line 128 does not change, so no existing custom workflow's loop semantics shift. If the naming grates, rename the output to `has_input` across both producers in one pass — but do it deliberately, not as a side effect of this change.
- **`runtime` as a namespace name is a land grab.** It is broad enough that future non-wait features will want to put things in it, and there is no plan here for what else lives there. Worth either narrowing it now (`wait.agents.pending`) or deciding deliberately that it is the general live-state namespace.
- **The concurrency cap is a guess.** 8 is a starting number, not a derived one. It should be a config value and it should be revisited with real fan-out data rather than defended.
- **Phase 1 might be the whole win.** It is worth honestly checking after Phase 1 whether `spawn_list` + `spawn_output` plus existing turn-batching already covers most orchestration pain. If it does, Phases 3–4 get a much higher bar to clear. I do not think it will — the dead-time problem is structural — but the phasing is deliberately ordered so that question is asked with data rather than assumed away.

---

## 10. Change inventory

### New files

| Path | Contents |
|---|---|
| `internal/db/migrations/postgres/<ts>_add_agent_messages.sql` | §5.1 |
| `internal/db/postgres/queries/agent_messages.sql` | enqueue / drain / list |
| `internal/db/core/agent_message.go` | model + kind/status enums |
| `internal/db/repository_agent_messages.go` | repo methods |
| `internal/llm/tools/spawn_list.go` | `spawn_list` |
| `internal/llm/tools/spawn_output.go` | `spawn_output` |
| `internal/llm/tools/spawn_wait.go` | `spawn_wait` (model on `bash_wait.go`) |
| `internal/llm/tools/spawn_send.go` | `spawn_send` |
| `internal/llm/tools/spawn_cancel.go` | `spawn_cancel` |
| `internal/workflow/runtime/wait_node.go` | the generic blocking node (§6.3) |
| `internal/workflow/runtime/wait_providers.go` | provider interface + `runtime` namespace assembly |
| `internal/workflow/runtime/activities/handlers/drain_agent_messages.go` | `DrainAgentMessages` |
| `internal/workflow/runtime/spawn_registry.go` | live-agent registry + `DoneCh` (model on `RunningInlineWorkflow`, `workflow.go:172-184`) |

### Modified

| Path | Change |
|---|---|
| `internal/workflow/runtime/workflow.go:2373` | `executeSpawnInline` gains a detached mode |
| `:2781-2923` | `executeToolsWithSpawnSupport` settles immediately for `wait=false` |
| `:2246-2258` | `spawnChildWorkflowConfig` gains `wait` |
| `:2260-2332` | `parseSpawnToolCall` parses `wait` |
| `:136-170` | `ChildWorkflowTracker` hosts the spawn registry |
| `internal/workflow/runtime/inline_workflow_executor.go:640-666` | drain mailbox at step boundary |
| `internal/workflow/runtime/loop_executor.go:492-511` | `while` sees the `wait` node's `runtime` snapshot |
| `internal/workflow/builtin/agent.yaml:128-226` | `wait` node + edge + outputs |
| `internal/workflow/model/constants.go:8-12` | `NodeTypeWait` |
| `proto/reliant/v1/workflow_v2.proto:254-262, ~460` | `WaitArgs{until, timeout, poll}` in the node `oneof` |
| `internal/workflow/runtime/activities/register.go:31` | register the node |
| `internal/workflow/runtime/step_executor.go:300` | dispatch |
| `internal/workflow/analysis/analyzer.go:539` | treat as a gating node |
| `internal/workflow/parser` (validation) | reject `wait` without `timeout`; validate `runtime.*` paths (§6.6) |
| `internal/workflow/reference/stub.go` | `runtime` namespace in CEL reference / completions |
| `handlers/call_llm.go:1237-1277` | rewrite description; add `wait`; expose new tools |
| `handlers/call_llm.go:274-276` | depth gating for `spawn_send` at depth 1 |
| `internal/db/postgres/queries/tool_calls.sql:55-85` | stranded query → mailbox anchor (§7.1) |
| `internal/workflow/reconciliation/reconciler.go:1477-1520` | new sweep |
| `internal/llm/tools/names/names.go` | new names; drop `ToolAgent` |
| `internal/llm/tools/registry.go`, `permissions.go:79` | register + permission floors |
| `internal/db/migrations/postgres/<ts>_add_approvals_thread_id.sql` | §7.6 |
| `web/src/…` (6 files, §7.3) | status keyed to thread, not tool call |

### Deleted

- `internal/workflow/runtime/pending_tool_results.go` (dead; or wired into the `wait=true` path)
- `internal/llm/tools/factory.go:321-339` + `names.go:49` + `permissions.go:79` `ToolAgent` arm

---

## 11. Test plan

Anchored on the failure modes above, not on the happy path.

1. **Lifetime**: LLM emits no tool calls with 2 agents live → loop must not exit; both results must be delivered. This is the regression that matters most.
2. **No spin**: same scenario asserts **zero** additional `call_llm` invocations while blocked in the `wait` node.
2b. **Wait node, in isolation** (independent of spawn): `until` false + `timeout` short → returns `timed_out: true`, does not throw. `until` already true at entry → returns immediately without registering a timer. Condition flips via a provider event → wakes on the event, not on the poll timer. Replay of a run containing a `wait` node produces identical history.
2c. **Validation**: a `wait` node with no `timeout` fails workflow parse. A condition referencing `runtime.agents.pendign` fails workflow parse rather than hanging (§6.6).
3. **Ordering invariant**: queue a message mid-turn, between an assistant-with-tool_calls and its tool_results. Assert the drained message lands *after* the tool results and that history passes the `call_llm.go:198-210` check.
4. **Stranded repair under async**: kill the worker between child-terminal and completion-enqueue; assert the new sweep produces exactly one `kind=2` row and does not fabricate one for a still-running child.
5. **Fail-closed**: child at status 2 (running) or 6 (paused) must never produce a synthetic completion.
6. **`spawn_wait` timeout is not an error**: assert `timed_out: true`, agent still alive, non-error result.
7. **Send to a finished agent** returns the `spawn(agent_id=…)` guidance, not a silent no-op.
8. **Cancellation**: `spawn_cancel` on an async agent stops it at a step boundary and enqueues `kind=3`.
9. **Backward compatibility**: every builtin workflow using `spawn` behaves identically with `wait` defaulted true — snapshot-compare against current fixtures.
10. **Cap**: N+1 spawns past the limit returns the capacity message as a normal result.
