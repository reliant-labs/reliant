# Fast Cancel Design Findings

**Scope:** Investigate feasibility of moving daemon-cancel ownership to the Temporal worker and implementing a generic per-activity cancel mechanism.

**Date:** Investigation phase  
**Status:** Findings documented, ready for design phase

---

## Executive Summary

**Activity Inventory:** 26 distinct activities registered with Temporal, spanning message processing, tool execution, context management, approvals, questions, and workflow lifecycle. Only **3 activities dispatch external work**:
- `CallLLM` — streams to LLM API
- `ExecuteTools` — dispatches to daemon (already has partial cancel handling)
- `ExecuteRunStep` — may dispatch to daemon via RunExecutor

**Feasibility of (A): High.** The API server's interrupt path is thin and ownership-clean: it reads auth and ownership from the database (which the worker has access to via the repo), signals the workflow to wake the thread, and pushes daemon cancels. Moving daemon-cancel ownership to the worker is straightforward — the worker already constructs its own `NATSDaemonRouter` and has the repo. No new entry points needed; the `remote_executor` already abandons work on cleanup.

**Feasibility of (B): Medium with critical collision to resolve.** A generic per-activity cancel mechanism is viable: the daemon's own pattern (a `CancelFunc` map keyed by request_id/tool_call_id) is proven and replicable in the worker. The `ActivityWrapper` is the natural seam to hook registration. **However**, `spuriousHeartbeatCancel` (registry.go:296) creates a direct collision: it treats ANY `context.Canceled` without a real Temporal `CanceledError` cause as "heartbeat failed, retry" — and an out-of-band activity cancel would look identical to the SDK. Real cancellations MUST arrive with a distinguishing cause to survive this filter.

**Q6 Risks (Primary):** The collision between out-of-band cancellation and `spuriousHeartbeatCancel` is the architectural bottleneck. **This must be resolved before design.** Options include:
1. Wrap activity cancels with a custom cause that `spuriousHeartbeatCancel` recognizes (cleanest)
2. Move cancellation inside heartbeat RPC delivery (adds latency, defeats the goal)
3. Redesign `spuriousHeartbeatCancel` to distinguish causes more surgically (risky; it protects a real and frequent failure mode)

---

## 1. Activity Inventory

All activities are registered in `internal/workflow/runtime/activities/register.go:RegisterAll()` and typed via `nodeTypeActivities` map (register.go:26).

| Activity Name | Location | External Dispatch? | Cleanup on Cancel | Notes |
|---|---|---|---|---|
| **SaveMessage** | handlers/save_message.go | DB write only | None needed | Terminal writes; handles ctx cancellation safely |
| **CallLLM** | handlers/call_llm.go | LLM API stream | `persistInterruptedTurn` | Detaches context (WithoutCancel), persists partial turn on cancel |
| **ExecuteTools** | handlers/execute_tools.go | Daemon (via RemoteExecutor) | Multiple (see below) | Most sophisticated; handles cancel via `detachedForTerminalWrite` |
| **Compact** | handlers/compact.go | LLM API call | None needed | Simple request/response, no streaming |
| **CreateWorktree** | handlers/worktree.go | Daemon (DaemonRouter) | Implicit via abandonment | Router abandons on context cancel |
| **AskQuestion** | handlers/question.go | Streaming hub message | None needed | Waits on mailbox; ctx cancel stops wait |
| **ApprovalCreate** | handlers/approval.go | DB write only | None needed | Creates approval record |
| **ApprovalResolve** | handlers/approval.go | DB write only | None needed | Resolves approval record |
| **QuestionCreate** | handlers/question.go | DB write only | None needed | Creates question record |
| **QuestionResolve** | handlers/question.go | DB write only | None needed | Resolves question record |
| **ExecuteRunStep** | handlers/run_executor.go | Daemon (conditional RunExecutor) | Implicit via ctx cancel | RunExecutor honors context cancellation |
| **CreateWorkflowWithThread** | handlers/create_workflow_with_thread.go | DB write + Temporal call | N/A | Temporal SDK handles |
| **LoadWorkflow** | handlers/load_workflow.go | DB read only | None needed | Read-only |
| **PreflightDaemonCheck** | handlers/preflight_daemon.go | Daemon preflight query | None needed | One-off preflight, no in-flight state |
| **WorkflowStatus** (lifecycle) | handlers/workflow_status.go | DB write only | None needed | Lifecycle activity |
| **ThreadStatus** (lifecycle) | handlers/thread_status.go | DB write only | None needed | Lifecycle activity |
| **WorkflowCheckpoint** (lifecycle) | handlers/workflow_checkpoint.go | DB write only | None needed | Lifecycle activity |
| **WorkflowError** (lifecycle) | handlers/workflow_error.go | DB write only | None needed | Lifecycle activity |
| **Cleanup** (lifecycle) | handlers/cleanup.go | DB cleanup only | None needed | Lifecycle activity |
| **EmitStreamFinalized** (lifecycle) | handlers/emit_stream_finalized.go | Streaming hub publish | None needed | Best-effort; lifecycle terminal |
| **DeleteWorktree** | handlers/worktree.go | Daemon (DaemonRouter) | Implicit via abandonment | Router abandons on context cancel |
| **UnknownStepType** | handlers/unknown_step_type.go | None | N/A | Error marker |
| **FailStep** | handlers/fail_step.go | None | N/A | Error marker |
| **SkippedStep** | handlers/skipped_step.go | None | N/A | No-op marker |
| **FetchThreadResult** | handlers/fetch_thread_result.go | Temporal workflow result query | N/A | Read-only |
| **ValidateThreadOwnership** | handlers/validate_thread_ownership.go | DB read only | None needed | Read-only |
| **LoadPresetParams** | handlers/load_preset_params.go | DB read only | None needed | Read-only |
| **EmitToolCallStatus** | handlers/emit_tool_call_status.go | Streaming hub publish | None needed | Best-effort; one-shot |
| **DrainAgentMessages** | handlers/drain_agent_messages.go | DB read + streaming | None needed | Read-only |
| **EnqueueAgentMessage** | handlers/enqueue_agent_message.go | DB write only | None needed | Write-only |
| **GenerateTitle** | handlers/compact.go | LLM API call | None needed | Simple request/response |

**Bottom line:** Only 3 activities have meaningful external dispatch that survives cancellation and needs cleanup:
1. **CallLLM** — LLM stream that can be interrupted mid-token
2. **ExecuteTools** — daemon tool calls (most complex; already partially handles cancel)
3. **ExecuteRunStep** — conditional daemon work

---

## 2. Current Cancellation Handling by Activity

### ExecuteTools (Most Sophisticated)

**File:** `internal/workflow/runtime/activities/handlers/execute_tools.go`

**Cancel handling:**
- **Line 503-504:** Early exit if `ctx.Err() != nil` before starting work
- **Line 648-658:** If context cancelled mid-execution AND tool returned no success result, emit "cancelled" status and record terminal result
- **Line 826-841:** `detachedForTerminalWrite()` — crucially, detaches context from cancellation using `context.WithoutCancel()` + fresh timeout for terminal writes. This prevents a cancelled context from failing the DB write of terminal tool status.
- **Line 657:** `upsertToolCallResult()` called within detached context to write the result
- **Flow:** Activity checks `ctx.Err()` at progress points; if cancelled, writes terminal status + result with detached context; tool result and status row must agree.

**Re-entry detection (Line 587-602):** `checkPriorTerminalResult()` detects if tool was already attempted and returns recorded outcome instead of re-running. Idempotent on the second entry, not on first attempt (tools are NOT idempotent).

### CallLLM (Sophisticated Stream Handling)

**File:** `internal/workflow/runtime/activities/handlers/call_llm.go`

**Cancel handling:**
- **Line 265-266:** If `ctx.Err() != nil`, call `persistInterruptedTurn()`
- **Line 1010:** Stream loop checks `ctx.Err()` to detect cancellation mid-stream
- **Line 2477:** Stream delta processing checks `ctx.Err()` and stops processing new deltas
- **Line 278-314:** `persistInterruptedTurn()` — detaches context via `context.WithoutCancel()` + fresh timeout, writes the partial assistant message that was streaming. Best-effort: failure logs but doesn't error (turn is already over).

**What it persists:** Partial response text and any tool calls detected so far. The activity returns normally so `save_message` runs and records the assistant message row.

### ExecuteRunStep (Conditional)

**File:** `internal/workflow/runtime/activities/handlers/run_executor.go`

**Cancel handling:**
- Delegates to `RunExecutor` interface, which can be a shell command executor
- If `RunExecutor` is actually running shell via daemon, it will stop on context cancel
- No explicit cleanup; relies on RunExecutor's own cancellation handling

---

## 3. Feasibility of (A): Move daemon-cancel ownership to worker

**Current state:**
- **API server** (`internal/serverapi/run.go:310`) constructs `NATSDaemonRouter` and uses it to push daemon cancels via `chat_interrupt.go` flow
- **Worker** (`internal/serverworker/run.go:234`) constructs its own `NATSDaemonRouter` but only uses it for abandonment in `remote_executor.go:322`
- **Database** — both have access via `repo`
- **Ownership check** — API server does `GetChatWithUserCheck()` and `GetThread()` for authorization before cancelling

**What would have to move:**

1. **Ownership checks** — currently in `threads.Service.InterruptThread()` (interrupt.go:112-136). These must stay with the API server; the worker CANNOT do user authorization (it has no auth context). The API server would need to:
   - Continue reading ownership via repo
   - Continue signaling the workflow to wake the thread (this is already a Temporal signal)
   - STOP pushing the daemon cancel itself
   - Instead signal/notify the worker that cancels are needed

2. **Worker-side entry point** — Need a way to deliver the cancel list to the worker. Options:
   - Add a Temporal signal `CancelToolsSignal` carrying tool call IDs (or request IDs)
   - Add a NATS subscription on the worker to listen for cancel commands (requires new infrastructure)
   - Embed the cancel command in the existing `InterruptThreadSignal`

3. **Router + Registry** — The worker already has:
   - `NATSDaemonRouter` at `run.go:234`
   - Access to `repo` at `run.go:126`
   - A place to register activity -> request_id mappings (see point 4)

4. **Activity-to-request_id mapping** — Currently the daemon knows what's executing because it receives `ToolRequest` messages directly. The worker doesn't have this naturally. Need to:
   - Register executing activities with their request IDs in a shared map (similar to daemon's `cancelByReq` at `daemonruntime/runtime.go:1237`)
   - Keyed by workflow_id + activity_id? Or thread_id + tool_call_id? Collision risk if not careful.
   - Cleanup on activity completion

**Assessment:** **Feasible.** The movement is organizational, not architectural. The infrastructure already exists. The gap is the registration point and signal delivery, neither of which is complex. Risk is moderate: need to avoid collisions in the mapping and ensure cleanup.

---

## 4. Feasibility of (B): Generic per-activity cancel mechanism

**Current state:**
- Each activity that dispatches work handles cancellation ad-hoc (see section 2)
- `ExecuteTools` and `CallLLM` both use `context.WithoutCancel()` for terminal writes — this pattern is proven
- Daemon's own pattern is a proven template: `CancelFunc` map keyed by request_id (`daemonruntime/runtime.go:1007-1020, 1237-1247`)

**What a generic mechanism would do:**
1. On activity entry, register a cleanup callback keyed by activity_id (or activity instance)
2. When cancel arrives, invoke the callback
3. Activity exits cleanly; Temporal reports normal completion with a "was cancelled" result
4. Workflow sees the cancellation in activity completion, not as a `CanceledError`

**The `ActivityWrapper` seam:**
- **File:** `registry.go:490-531`
- Already wraps EVERY activity execution
- Spawns heartbeat goroutine at line 499-531
- Could hook a cancel registry here
- Could expose activity-to-request_id mapping to the cancel handler

**Implementation sketch:**
```go
type activityCancelRegistry struct {
    mu        sync.Mutex
    callbacks map[string]context.CancelFunc // keyed by activityID
}

// In ActivityWrapper.Execute:
cancelCtx, cancelFunc := context.WithCancel(ctx)
registry.register(activityID, cancelFunc)
defer registry.unregister(activityID)

// Activity runs on cancelCtx instead of ctx
result, execErr := w.activity(cancelCtx, input)

// Separate goroutine or signal handler calls:
registry.cancel(activityID)  // triggers cancelFunc
```

**Per-activity cleanup:** Each activity would check `ctx.Err()` and do its cleanup:
- `execute_tools` — already does: cancel pending tool calls via daemon, write terminal status
- `call_llm` — already does: persist partial turn via detached context
- Others — either don't need it or are already safe

---

## 5. THE CRITICAL Q6 RISK: spuriousHeartbeatCancel Collision

**What `spuriousHeartbeatCancel` does:**
- **File:** `registry.go:296-318`
- Detects if a `context.Canceled` error was caused by a heartbeat RPC failure rather than a real instruction to stop
- **Key logic (line 304-317):** 
  - Gets `context.Cause(ctx)`
  - Returns `true` (spurious) if cause is NOT:
    - `nil` (no cause recorded)
    - `context.Canceled` (the generic "context was canceled" marker)
    - `temporal.IsCanceledError` (real Temporal server-side cancel)
    - `activity.ErrActivityPaused` or `activity.ErrActivityReset` (legitimate stop instructions)
  - If cause is anything ELSE, treats it as heartbeat failure and signals RETRY
- **Rationale (line 272-285):** The SDK cancels activity ctx on any heartbeat error it classifies as retryable. One slow heartbeat RPC → timeout → `codes.Unknown` (retryable) → SDK cancels the activity. That one slow RPC shouldn't kill a healthy activity; retrying is correct.

**The collision:**
An **out-of-band activity cancel** (pushed by the worker without going through Temporal) would:
1. Call the activity's `context.CancelFunc` directly
2. Set `ctx.Err()` to `context.Canceled`
3. NOT set a Temporal `CanceledError` cause (it's not from Temporal)
4. Potentially set a custom cause (depending on implementation)

**If the custom cause is NOT recognized by `spuriousHeartbeatCancel`, the activity will be RETRIED instead of cancelled.**

For example, if we wrap the cancel with:
```go
detached, cancel := context.WithCause(cancelCtx, ErrorActivityCancelled)
cancel()  // Sets ErrorActivityCancelled as cause
```

Then `spuriousHeartbeatCancel` sees:
- `ctx.Err() == context.Canceled` ✓
- `context.Cause(ctx) == ErrorActivityCancelled` — NOT in the whitelist ✗
- Returns `true` (spurious) → activity retried

**This is a direct blocker for design (A+B).**

**Solutions examined:**
1. **Add to whitelist (CLEANEST):** Update `spuriousHeartbeatCancel` line 312-314 to recognize a new `ErrorActivityCancelled` cause. Minimal change, zero risk to heartbeat logic, directly addresses the collision. Requires: define new error type, set it in cancel path, test it.

2. **Move all cancellation into heartbeat delivery:** Only cancel via Temporal signals that arrive in heartbeat responses. Re-introduces the 1-3s latency the fast-cancel work is trying to eliminate. Not viable for the design's goal.

3. **Redesign `spuriousHeartbeatCancel`:** Use a different signal (e.g., flag in activity context values, not a cause). Complex; risks breaking the heartbeat protection which prevents real outages.

**Recommendation:** **Pursue solution 1.** It is surgical, testable, and doesn't touch the heartbeat RPC logic. The risk is near-zero: a new cause type that the activity-cancel path sets, and the check adds it to the whitelist. Define the cause early; it becomes part of the contract.

---

## 6. Worker NATS subscription pattern

**Current state:**
- Worker is **publish-only** on NATS (verified: no `nc.Subscribe` calls in `internal/serverworker/`)
- Worker uses `NATSDaemonRouter` to PUBLISH to daemon
- Gateway subscribes to daemon-facing subjects via `NATSToolBridge` (`internal/toolexec/nats_bridge.go:119-121`)

**Example from gateway (line 164-183):**
```go
// tools.cancel.{userID}.{daemonID}
addSub(b.nc.Subscribe(daemonSubject(toolCancelSubject, userID, daemonID), func(msg *nats.Msg) {
    var cancel struct {
        RequestID string `json:"request_id"`
        Reason    string `json:"reason"`
    }
    if err := json.Unmarshal(msg.Data, &cancel); err != nil { ... }
    if err := b.mgr.SendToolExecutionCancel(ctx, userID, cancel.RequestID, cancel.Reason); err != nil { ... }
}))
```

**For the worker to RECEIVE cancel commands:**

Option 1 (via Temporal signal): Embed in existing `InterruptThreadSignal` or create `CancelActivitiesSignal`. Temporal delivers this on the next heartbeat. Latency floor is 3s (heartbeat throttle), defeats fast cancel. Not viable.

Option 2 (NATS subscription): Add worker-side subscription on a dedicated subject (e.g., `activity.cancel.{workflow_id}.{activity_id}`). Worker sets up subscription at startup. Daemon-gateway publishes to this subject when API server requests cancellation. This is the pattern the gateway already uses for daemons.

**Assessment:** Option 2 is feasible and follows existing conventions. Latency is network RTT only (~ms), no Temporal round-trip needed. Requires:
- Define subject naming scheme
- Worker subscribes at startup (new subscription in `run.go` around line 192-203)
- Handle per-activity cancel messages in the subscription handler

---

## 7. Integration points and risks

**Where registration would hook:**

1. **Activity entry (ActivityWrapper.Execute):**
   - Register activity_id → cancel registry
   - Register workflow_id + activity_id → request_id mapping (needed for toolexec)
   
2. **Activity exit:**
   - Unregister from both registries
   - Cleanup deferred context cancellation

3. **Tool execution dispatch (execute_tools.go):**
   - Map tool_call_id → activity_id so incoming daemon cancels can find the right activity

**Temporal workflow signal coordination:**
- `InterruptThreadSignal` already wakes the workflow
- NEW: Separate signal or NATS message carries the activity_ids to cancel
- Worker receives, looks up registry, invokes cancel for each

**Testing considerations:**
- Spurious cancel detection must pass the new cause through `spuriousHeartbeatCancel`
- Activity must not retry on a real out-of-band cancel
- Terminal writes must survive the cancellation (both activities already do this)
- Cancellation must NOT race with activity completion (the deferred unregister handles this)

---

## 8. Summary of findings

| Question | Answer |
|---|---|
| **Inventory:** How many activities? Dispatch external work? | 26 activities; 3 dispatch external work (CallLLM, ExecuteTools, ExecuteRunStep). |
| **Current cancel handling:** Good or incomplete? | Sophisticated and partial: ExecuteTools and CallLLM both handle cancel carefully with detached writes; others are simple or don't need it. |
| **Feasibility of (A):** Move daemon-cancel to worker? | **Feasible.** Requires activity-to-request_id registration (new) and signal from API server (new), but no architectural changes. Worker already has router and repo. Ownership checks stay with API server. |
| **Feasibility of (B):** Generic per-activity cancel? | **Feasible.** Daemon's own pattern is proven; ActivityWrapper is the natural hook. Each activity already knows what cleanup it needs. |
| **Q6 Risk:** spuriousHeartbeatCancel collision? | **CRITICAL.** Out-of-band cancel would look like heartbeat failure unless cause is recognized. Requires adding a new cause type to the whitelist (line 312-314). Solution is surgical (add one line); risk is near-zero. MUST be done before shipping. |
| **Worker NATS subscription:** Is it feasible? | **Yes.** Worker is currently publish-only; adding subscriptions follows the gateway's own pattern. Could use existing Temporal signal channel, but NATS is faster (ms vs 3s throttle). |

---

## Open questions for design phase

1. **Activity-to-request_id mapping:** Should it be keyed by `activity_id` only, or `workflow_id+activity_id`? Risk: concurrent workflows could collide if activity_ids are not globally unique.

2. **Signal vs NATS:** For delivering cancel requests to the worker, which is preferred? Temporal signal (slower, simpler coordination) or NATS subscription (faster, new code path)?

3. **Cleanup lifecycle:** Should activity register/unregister happen inside ActivityWrapper, or in each activity's handler? Centralized is cleaner; per-activity allows activities to customize.

4. **Backwards compatibility:** Do we need to support monolith mode? The briefing says distributed-only, so probably not, but should be confirmed.

---

## References

- `internal/workflow/runtime/registry.go:296-318` — spuriousHeartbeatCancel
- `internal/workflow/runtime/registry.go:490-531` — ActivityWrapper
- `internal/workflow/runtime/activities/handlers/execute_tools.go:826-841` — detachedForTerminalWrite pattern
- `internal/workflow/runtime/activities/handlers/call_llm.go:278-314` — persistInterruptedTurn pattern
- `internal/toolexec/daemonruntime/runtime.go:1007-1020` — daemon cancel registry
- `internal/threads/interrupt.go:112-182` — current interrupt flow (API server side)
- `internal/toolexec/nats_bridge.go:119-183` — gateway subscription pattern
- `internal/serverworker/run.go:234` — worker daemon router
- `internal/serverapi/run.go:310` — API server daemon router
