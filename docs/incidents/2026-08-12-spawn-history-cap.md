# Incident: async spawns stranded by Temporal history-cap termination

Chat `0dc5167e-b268-4661-a863-5a120f43b78e`, 2026-08-12.

This file is the shared briefing for the follow-up work. Everything in
"Verified facts" was measured against the live system — **do not re-derive it.**

## Environment

The chat lives in the **control-plane-backed** `reliant` DB:

```
PGPASSWORD=postgres psql -h localhost -p 5434 -U postgres -d reliant
```

Not port 5433 (this repo's own compose Postgres — it has no `chats` table).
Temporal for this stack is on **localhost:7233**, namespace `reliant`.

## Verified facts — settled, do not re-investigate

### Root cause

Temporal terminated the parent workflow:

```
eventId: 51199
eventType: EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED
reason:  "Workflow history count exceeds limit."
identity: history-service
```

`HistoryLength 51199` against the server's stock `limit.historyCount.error`
of **51,200**. `HistorySize` was 33.4 MB, well under the 50 MB size cap — the
**count** bound first.

### Why both spawns died

A background spawn is **not** a child workflow. `dispatchSpawnBackground`
(`internal/workflow/runtime/workflow.go:2932`) runs each as a `workflow.Go`
goroutine *inside the parent's execution*. Terminating the parent kills every
detached spawn instantly, with no chance to report.

### Why continue-as-new never fired

`readyToContinueAsNew` (`internal/workflow/runtime/continue_as_new.go:102`)
requires **both** ≥40,000 events **and** quiescence (zero live detached
spawns). Those never held simultaneously:

| Time (UTC) | Event |
|---|---|
| 05:12:51 | `7cef944a` spawned — live until termination |
| 05:28:25 | crossed the 40,000-event threshold |
| 05:28:47 | `955f41b7` spawned — live until termination |
| 05:56:43 | terminated at 51,199 events |

From threshold to cap the run burned 11,199 events in 28 minutes with at
least one spawn always live. The gate is unsatisfiable for a continuously-
spawning parent.

### History economics (measured from the real 51,199-event history)

**Exactly 6 events per activity** (`ActivityTaskScheduled/Started/Completed`,
each preceded by `WorkflowTaskScheduled/Started/Completed`).

| Activity | Calls | Events | Input |
|---|---:|---:|---:|
| SaveMessage | 2693 | 16,158 | 7.9 MB |
| DrainAgentMessages | 1350 | 8,100 | 0.3 MB |
| CallLLM | 1350 | 8,100 | 2.3 MB |
| EmitStreamFinalized | 1349 | 8,094 | 0.4 MB |
| ExecuteTools | 1330 | 7,980 | 2.6 MB |
| WorkflowCheckpoint | 128 | 768 | ~0 |
| *(all others)* | 124 | 744 | — |

One agent turn, in order:

```
SaveMessage → DrainAgentMessages → [SideEffect marker] → CallLLM
  → SaveMessage → EmitStreamFinalized → ExecuteTools → SaveMessage
```

= **6 activities + 1 marker ≈ 37 events/turn**. Event-type totals:
8,324 `ACTIVITY_TASK_SCHEDULED`, 8,287 `WORKFLOW_TASK_SCHEDULED`,
1,355 `MARKER_RECORDED` (1,350 SideEffect + 5 Version).

**There are ZERO uses of `ExecuteLocalActivity` in the codebase.** A local
activity batches into a marker (~1 event) instead of 6. This is the single
biggest available lever.

**Checkpoints are not the problem**: 128 calls / 768 events = **1.5%** of
history. They are written at node-entry and loop-iteration boundaries only,
never per activity.

### Local-activity conversion — what landed, and why SaveMessage did not

`DrainAgentMessages` and `EmitStreamFinalized` (success path only) now dispatch
via `workflow.ExecuteLocalActivity`, behind gates `agent-mailbox-drain` v2 and
`stream-finalized-local`. Measured on regenerated fixtures: **agent_tool_loop
119 → 101 events, structured_agent_loop 131 → 113, spawn 235 → 197** — 6 events
saved per converted call, ~12 per agent turn (**37 → ~25**, a 32% cut).

Two constraints that any further conversion must respect, both verified against
the SDK source (v1.37.0) rather than inferred:

1. **Local-activity arguments are passed by REFLECTION, not deserialized.**
   `executeFunction` does `fnValue.Call(reflectArgs)`, so the value the workflow
   passes must already BE the registered parameter type. The old
   `map[string]interface{}` inputs panic with `reflect: Call using X as type Y`.
   This is why `DrainAgentMessagesInput` / `EmitStreamFinalizedInput` moved to
   `activities/types` and the handlers re-export them as type ALIASES — an
   alias preserves `reflect.Type` identity; a defined type would not. Test
   stubs must be updated to the real type for the same reason.

2. **`SaveMessage` must NOT become a local activity.** Its idempotency key is
   `workflowID-runID-activityID` (`handlers/save_message.go:112`), and the two
   dispatch modes draw activity IDs from DIFFERENT counters: regular activities
   use the command event id (observed 5, 11, …, 52, 70, 95 in a real history),
   local activities use `localActivityCounterID`, which restarts at 1, 2, 3…
   per run. Converting some SaveMessage call sites and not others therefore
   makes a local `SaveMessage` collide with an early regular activity's key —
   and the collision is not a benign dedupe: on `AttemptNumber > 1`
   `checkExistingMessage` **DELETES** the colliding message and recreates it
   (`threads/save_message.go:339`). Silent message loss in a user's chat.
   Converting SaveMessage safely means changing the idempotency key first, so
   it is a separate change with its own reasoning, not a mechanical follow-on.

The terminal finalize path (`finalizeOutstandingStreams`, reached from
`handleWorkflowCompletion` on cancel/error/panic) deliberately stays a regular
activity: a local activity executes as part of a workflow task, and a closing
execution may never get another one. It fired **once** in this whole history,
so leaving it durable costs nothing.

### Two recovery gaps

**Gap 1 — thread status is never cascaded.**
`CascadeTerminalStatusToDescendants` updates `workflows` only. Live DB:

```
threads.status=2 (running) under a TERMINAL workflow: 288 rows
  174 completed | 64 cancelled | 50 failed
```

All 3 threads of this chat are still `status=2`, `completed_at` NULL.

**Gap 2 — repair reports are undeliverable, and Gap 1 hides them.**
`repairStrandedBackgroundSpawns` enqueued rows `70234ee7` and `4d4aa023`
addressed to the (dead) parent thread. Both are still `status=1` (queued),
`delivered_at` NULL. Delivery happens only in `drainAgentMessagesAtBoundary`
at a live loop-step boundary, so they can never arrive. The backstop that
should catch this, `ListThreadsWithOrphanedAgentMessages`, only matches
threads in a **terminal** status — which Gap 1 prevents. The repair also
never clears `tool_calls.status=6` (backgrounded).

**Gap 3 — the user was never told.** `WorkflowError` fired exactly **once**
in the whole history. On a hard terminate the worker gets no further workflow
task, so no `handleWorkflowCompletion` path runs. The reconciler is the only
component that notices (`TERMINATED → Failed`, `reconciler.go:541`) and it
writes no `chat_updates` row.

## Spawn state is reconstructible (relevant to the redesign)

`runSpawnInlineChild` (`workflow.go:2749`) already rebuilds a **brand-new**
`InlineWorkflowExecutor` with **fresh node outputs** and re-executes on any
transient error. Its own comment: *"The thread's persisted messages ensure we
resume from where we left off."* So a spawn already survives losing all
in-memory state on a routine path.

Resumption is first-class: `parseSpawnToolCall` treats `agent_id` as
`isResumption` against an existing thread. `ChildWorkflowTracker.
liveDetachedSpawns` holds the four durable fields needed (`ToolCallID`,
`ChatID`, `ParentThread`, `ChildThread`), already sorted by tool-call id for
replay determinism.

**Known gap:** spawn threads have **no** position checkpoint —
`workflow_checkpoints` is keyed by `workflow_id` and only the root run writes
one. Verified: 12 spawn threads in this chat, exactly 1 checkpoint row
(`agent_loop`, iteration 126, the parent).

## Hard constraint for anything touching the command sequence

Any change to the sequence of workflow commands **must** sit behind a
`workflow.GetVersion` gate, or replaying existing histories wedges with
TMPRL1100. Established pattern: `drainAgentMessagesVersionGate`
(`agent_mailbox.go:17`), `continueAsNewVersionGate`
(`continue_as_new.go:17`). This execution already carries five change IDs:
`preallocated-message-id-1`, `resume-hold-1`, `position-checkpoints-1`,
`continue-as-new-1`, `agent-mailbox-drain-1`.

Replay fixtures live in `internal/workflow/runtime/replaytest/`.

## Reproducing the measurements

```bash
temporal workflow describe --address localhost:7233 --namespace reliant \
  -w 0dc5167e-b268-4661-a863-5a120f43b78e

temporal workflow show --address localhost:7233 --namespace reliant \
  -w 0dc5167e-b268-4661-a863-5a120f43b78e -o json > /tmp/scratchpad/hist.json
```
