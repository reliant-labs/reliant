# Thread interrupt: "stop what you're doing and read this"

Status: in progress
Owner: this branch

## The user-facing gap

A user typing into a running agent has exactly one delivery semantic today:
queue it, and the agent reads it at its next loop-step boundary. That is
*steering* — "finish what you're doing, then read this."

The other thing users want is *interrupting* — "stop, this changes what you
should be doing." The UI already implies it exists: the "Send now" button says
"Pull this back out of the queue and send it as a new message now," and it stays
enabled while the agent is running. It does not do that. It claims the mailbox
row and replays it through the ordinary `SendMessage` path, which for a RUNNING
workflow does a bare `SaveMessageToThread` — no cancellation, no boundary wait,
and the agent still does not see it until its next `call_llm` reads history.

So mid-run "Send now" is steering with worse ordering (see "Why the current
Send now is actively wrong" below). Interrupt does not exist.

## Why the current "Send now" is actively wrong

`SendMessage`'s RUNNING branch writes straight into `messages`. If that lands
mid-turn the DB row order becomes:

    [assistant(tool_calls), user, tool_results]

`repairMessageHistory` (call_llm.go) rescues the LLM request by *reordering* —
it pre-indexes every tool result and emits them immediately after their
assistant message, pushing the user turn after them. Correct request, but
in-memory only: the DB keeps the interleaved order and the transcript renders
from that, so the user's bubble appears between a tool call and its results.

Worse, if the write lands while `execute_tools` is still running, history is
`[assistant(tool_calls), user]` with no results yet. Any reader loading history
at that instant sees dangling tool calls and synthesizes
`InterruptedToolResultContent` — "outcome unknown, verify before re-running" —
for tools that are running along fine. The main loop does not read at that
moment, so it does not bite today, but `compact` and discuss mode both load
history off the loop's cadence.

The mailbox exists precisely to prevent this (see
`20260811000000_add_agent_messages.sql`). "Send now" bypasses it.

## Cancel and pause are NOT the same, and the difference decides the design

Both cancel in-flight tool work. They are not interchangeable.

**Per-tool cancel** (`CancelToolCall`) sets an in-memory signal and asks the
daemon to stop one execution by tool-call id. The activity keeps running.
`handleToolExecutionResult` turns that into a real, durable per-tool outcome:
`upsertToolCall(..., ToolCallStatusCancelled, ...)` plus a returned
`message.ToolResult` reading "Tool execution cancelled by user". Every sibling
tool still finishes on its own merits — the code is explicit that a dead context
is not evidence about THIS tool, because siblings share one activity context.
`execute_tools` returns normally, the step's `save_message` runs, and
`tool_results` are persisted. **History stays valid by construction.**

**Pause** (`pauseCoordinator.cancelAll()`) cancels the shared *workflow* activity
context. Every in-flight activity at every nesting depth dies. `execute_tools`
returns a `temporal.CanceledError`, and `handleActivityCompletion` takes an
early return on that error — **before** `executeSaveMessage` runs. So the
activity produces no output and `tool_results` are never persisted. The thread
is left with an assistant message carrying tool_calls and no results row. That
is exactly the dangling-tool-call state, and it is why
`InterruptedToolResultContent` exists at all.

Pause gets away with it because resume re-`Start()`s the step and re-runs the
whole activity. Interrupt cannot: we do not want to re-run the tools, we want to
abandon them and move on. Reusing `cancelAll()` for interrupt would leave
dangling tool calls behind on every interrupt and lean on the repair pass to
synthesize "outcome unknown" — degrading the model's information on the most
common interrupt case.

**Conclusion: interrupt is built from per-tool cancel, not from pause.**

What interrupt *does* borrow from pause is the observation that cancelling from
a background goroutine reaches any graph shape. But per-tool cancel already has
that property for free: it targets tool-call ids, so it does not care whether
the graph has a `loop` node.

## Design

### Interrupt is a verb on the thread, not a property of a message

The alternative — a `delivery_mode` column on `agent_messages` — breaks on a
real case: the user queues a message, then decides "actually, send that one
now." Upgrading the row means `UPDATE ... SET mode='interrupt'`, which races the
drain, and is ambiguous when the queue holds three messages and only the middle
one is upgraded.

Modelling interrupt as a thread-level action makes that case trivial and
race-free. The mailbox row is always just "text for this thread." Interrupting
sweeps whatever the mailbox holds, oldest-first — which is the order the user
typed. Nothing to update, no race, no ambiguity.

This also deletes `ClaimQueuedAgentMessages` and its claim-then-resend dance,
along with the frontend tombstone machinery that exists only to paper over that
path's poll lag.

### Sequence

`InterruptThread(chat_id, thread_id)`:

1. Find every tool_call on the thread in EXECUTING state.
2. Cancel each via the existing per-tool path (in-memory signal + daemon
   cancel). Each yields a durable cancelled status and a real tool_result.
3. `execute_tools` returns normally; `save_message` persists tool_results.
4. Loop reaches the top, `call_llm` drains the mailbox, model sees cancelled
   results plus the user's message.

Nothing is bypassed and no repair is needed.

### Drain moves into call_llm

Delivery used to be `drainAgentMessagesAtBoundary`, called from
`loop_executor.go` at the top of each iteration. That function and its
`agent-mailbox-drain` version gate are **deleted** — not gated off, deleted.
Delivery now lives in `call_llm`, where `loadConversationHistory` already runs:

- Makes the invariant structural rather than conventional. At `call_llm` time
  history must already be consistent or the provider would reject it.
- Fixes workflows with no `loop` node — `gsd.yaml` and `one-ring.yaml` both use
  `call_llm` with no loop, so their queued messages are stranded forever today.
- Is idempotent under activity retry: the drain commits and marks rows
  delivered, so a retry finds an empty queue.
- Lets `loop_executor.go` stop knowing mailboxes exist.

No version gate. The old path is gone, and the recorded replay fixtures were
regenerated (`make replay-fixtures`) because they encode a drain command the new
code never issues. In-flight runs at deploy time wedge with TMPRL1100 and are
recovered by the reconciler + resume path — the accepted cost of cutting
cleanly. See `internal/workflow/runtime/replaytest/fixtures/README.md`.

### The while-condition term (the loop-exit gap)

`call_llm` drains the mailbox BEFORE it reads history, so a message queued
*during* the response is not part of that turn. If the model then returns no
tool calls, the loop exits on a thread still holding an undelivered message and
nothing is left to deliver it. That window is exactly when a user types —
watching a long final answer arrive.

Closed by `CallLLMOutput.pending_inbox`: after streaming completes, `call_llm`
re-checks the mailbox (one indexed SELECT) and reports whether anything landed.
Both agent loops OR it into their `while`, so the loop takes one more turn, and
that turn's drain delivers the message.

- `agent.yaml`: `... || outputs.pending_inbox == true`
- `structured-agent.yaml`: added INSIDE the `max_turns` conjunction — a queued
  message earns another turn, it must not buy unbounded ones.
- Both guard with `has(nodes.call_llm) && has(nodes.call_llm.pending_inbox)`,
  like every sibling output. A bare reference throws `no such key: call_llm` on
  an iteration that never reached the node, failing the whole output
  evaluation (caught by `TestStructuredAgentOutputCEL` and
  `TestSemanticParityFixtures`).

Once delivered the flag goes false, so the loop exits normally rather than
spinning on its own delivered message. Scoped per-thread, so a sub-agent's
mailbox never keeps the root loop turning.

### Why not an `agent` node

`agent` and `structured-agent` use identical node vocabulary and differ only in
arrangement. `auditing-agent` has TWO `call_llm` and TWO `execute_tools` nodes in
one loop. Encapsulating "the agent loop" as a node would have to re-expose every
difference as config, and would make custom approval/permission logic — today
just a node on an edge — into hooks. Rejected.

## Plan

1. **DONE** — Test that pins per-tool cancel's semantics: siblings survive, the
   cancelled tool yields a durable result, every call gets a result.
   `execute_tools_interrupt_test.go`. Mutation-verified: removing the
   `(execResult == nil || !execResult.Success)` guard fails
   `TestInterrupt_ContextCancellationDoesNotClaimCompletedSiblings`.
2. **DONE** — Drain moved from `loop_executor.go` into `call_llm`.
   `agent_mailbox.go` deleted outright, along with its version gate and the
   boundary-dispatch test. `call_llm_mailbox_drain_test.go`
   (mutation-verified), plus `root_thread_mailbox_drain_e2e_test.go` updated to
   assert the new contract. Replay fixtures regenerated and passing.
3. **DONE** — `InterruptThread` RPC (`internal/grpc/services/chat_interrupt.go`,
   proto in `chat.proto`). Cancels every EXECUTING tool call scoped to the
   thread, via the same per-tool path CancelToolCall uses; reports undeliverable
   cancels rather than counting them as success; leaves the mailbox for
   call_llm. `chat_interrupt_test.go`, mutation-verified on thread scoping
   (removing it fails `TestInterruptThread_LeavesOtherThreadsAlone`).
4. **DONE** — Frontend cut over to interrupt. `QueuedMessages` now offers one
   per-thread "Interrupt & send now" (only while the agent is running) instead
   of per-message "Send now" / "Send all". The client-side auto-flush is
   deleted (a live agent delivers in call_llm; an idle one is swept by
   `absorbQueuedMailbox` on the next send; genuinely undeliverable rows are
   marked by the reconciler). `ClaimQueuedAgentMessages` is deleted end to end:
   RPC, proto messages, handler, and its test file. Tombstones stay — the
   per-message CANCEL still races the drain and still needs them.
5. **DONE** — The while-condition term. See "The loop-exit gap" above.

Remaining: nothing in this plan. Possible follow-ups — surfacing interrupt from
the composer's stop button (today it is only on the queued strip), and deciding
whether an interrupt with an empty mailbox should be offered at all.

## Addendum: pause resumed by re-entering the node (fixed)

Found via chat 4d92f694. A `landing-page` run (loop node → inline sub-workflow →
forked implementer thread → two live spawns) was paused during the implementer's
first attempt. Resuming re-seeded `## Get It Right — Attempt 1 of 4` — the same
bytes (md5 `fccd21bc…`), thirty minutes later, at seq 111 beside the original at
seq 37.

Cause: pause cancels the shared activity context; the running activity returns a
`CanceledError`; and BOTH nested executors — `inline_workflow_executor.go` and
`loop_executor.go` — returned that error rather than handling it. The stack
unwound to the top-level `retryLoop` in `workflow.go`, which blocks until resume
and then calls `loopExecutor.Execute()` again, rebuilding every frame below it.
Anything a node does on ENTRY (fork a thread, evaluate `thread.inject`) ran a
second time.

This is also why it looked correct everywhere else: the TOP-LEVEL executor
already resumed in place (`workflow.go`, "Activity cancelled (pause), blocking
until resume then retrying"), so a plain agent loop — and a spawned sub-agent,
which is its own goroutine off the same shared context — behaved fine. Only
workflow NODES regressed.

Fix: both nested sites now do what the top level does — `DoCheckPause` in place,
then re-dispatch just the cancelled step via `executor.Start`, keeping the frame
and its position on the stack. No re-entry, no re-fork, no re-inject, and no
checkpoint reconstruction: position is preserved because it was never discarded.

The flat `workflow_checkpoints` row (`content_pass`, iteration 0) is a symptom
rather than the cause, and is left alone. It cannot express a nested position,
but nothing now depends on it for the pause path.

`nested_pause_resume_test.go` pins it: a pause during a nested step must leave
the node's on-entry side effect having run exactly once. Verified to fail before
the fix (`prepareRuns: 2`) and pass after, with the loop-executor half
independently mutation-checked.

## Backend status

Done and green (`./internal/grpc/... ./internal/workflow/...` on an isolated DB,
plus the replay-fixture suite). The remaining work is frontend-only, plus the
while-condition term.

Not yet wired: nothing calls `InterruptThread` — the RPC exists and is tested,
but the UI still uses claim-and-resend. Until step 4 lands, behavior is
unchanged from the user's point of view except that queued messages now deliver
via call_llm (which also fixes the gsd/one-ring stranding).

## Verification notes

Run tests against an ISOLATED database. The shared `reliant` DB on :5433 is used
concurrently by other agents, and a full-package run against it produced 15
failures on one run and 30 on the next — all cross-agent contention, none real.
The same suite on a private database is green:

    createdb -h localhost -p 5433 -U postgres interrupt_verify
    DATABASE_URL='postgres://postgres:postgres@localhost:5433/interrupt_verify?sslmode=disable' \
      go test ./internal/workflow/... -count=1
