# Interrupt & Pause — working spec

Terse on purpose. Update as we learn. Uncommitted branch work.

## ⛔ THE REQUIREMENT — stated by the user many times, read this FIRST

**Interrupt semantics MUST be EXACTLY the same as pause. Byte for byte the same
mechanism.** The ONLY difference is SCOPE: pause = whole chat/workflow, interrupt = one
thread. Nothing else differs. Not the settle path, not the re-dispatch path, not the
retry path.

**Tools are NOT idempotent and there is NO guarantee they ever will be.** A tool call that
has begun execution must NEVER be re-entered/re-executed on resume or on re-dispatch. This
protection has existed for 6+ months and is what is on `origin/main` today. When something
re-runs a tool, the bug is in whatever broke that protection — do NOT "fix" it by inventing a
new mechanism, and do NOT propose making interrupt skip re-dispatch while pause keeps it.
Find the existing protection and restore/extend it.

**Do not propose new mechanisms.** Every time a fix here has been invented rather than
derived from what main already does, it has broken something live. Research main first.

## LIVE FAILURE STILL OPEN (chat 432e6795-9bd6-48f3-bae8-d1870db804f4, 2026-08-17)

Interrupt during a RUNNING TOOL re-executed the tool. Trace:
```
17:32:06  ExecuteTools#53 SCHEDULED+STARTED (bash)
17:32:07.4 msg queued
17:32:08.6 interrupt -> CANCEL_REQUESTED on #53
17:32:08.6 ExecuteTools#63 SCHEDULED     <- re-dispatch ~10ms later
17:32:10   #53 COMPLETED                 <- predecessor settles 1.4s LATER
17:32:20.3 #63 COMPLETED                 <- RAN THE TOOL AGAIN, 12s
17:32:20.4 message delivered
```
`tool_calls` has ONE row, `started_at 17:32:08.607` — #63 overwrote #53's row.

**Why `checkPriorTerminalResult` did not stop it:** it only skips when the row is already
TERMINAL. At re-dispatch time (~10ms after cancel) the predecessor was still EXECUTING,
because cancellation only reaches the worker on a heartbeat (up to 3s). So the successor
re-ran. Removing `WaitForCancellation` is what exposed this — the wait had been guaranteeing
the predecessor settled first.

**Do NOT fix by treating EXECUTING as "don't re-run"** — that breaks legitimate retry after a
worker crash, where EXECUTING is exactly the state you must recover from.

**Interrupt-during-LLM works fine.** Only interrupt-during-tool is broken.

**The "call_llm without draining" report is NOT a second bug:** `CallLLM#87` scheduled 17:32:20,
message delivered 17:32:20.437 — the drain DID run inside call_llm as designed. The apparent
delay was the 12s tool re-run sitting in front of it. One cause, one symptom.

## RESEARCH DONE — main has NO tool re-entry guard. The protection is `checkPause` BLOCKING.

Ran the research the previous "NEXT STEP" asked for. The premise was wrong, and this changes
what the fix is. Evidence, all from `git show origin/main:...`:

1. **`attemptNumber` is dead code on main.** It appears at exactly three sites —
   declared `:241`, passed `:380`, received as a param `:485` — and `executeSingleTool` never
   reads it. Threaded, never used.
2. **`executeSingleTool` on main has no guard at all.** Its full body is: `ctx.Err()` check →
   unmarshal input → `loadToolExecutionContext` → daemon routing → `upsertToolCall(Pending)` →
   `executeToolWithStatus`. No `GetToolCall`, no `IsTerminal`, no dedup, no idempotency —
   grep for all of those in main's copy returns nothing. `checkPriorTerminalResult` does not
   exist on main (`grep -c` = 0); it is OURS.
3. **Main's own test says so, in words.** `execute_tools_idempotency_test.go:244-245`:
   `TestExecuteToolsActivity_NoReExecutionOnRetry` is
   `t.Skip("Skipping until idempotency is implemented - current implementation does not
   prevent re-execution")`. That skip is on `origin/main` today.
4. No guard in the executor layer either (`remote_executor.go` on main: no `GetToolCall` /
   `IsTerminal` / dedup).

**So what actually protected tools on main?** `checkPause` BLOCKS. On main the only cancel
source is pause, and the re-dispatch block (`workflow.go:1748-1774`) is:
`CanceledError` → `checkPause(ctx)` → *blocks until a human resumes* → `executor.Start`.
The predecessor activity therefore always settles (heartbeat, ≤3s) long before the successor
is dispatched, because the successor waits on a human. **Overlap was structurally impossible,
so main never needed a re-entry guard.** Re-running the tool *after* resume is main's
behavior and is defensible there — a paused tool was killed and produced no result, so
re-running it on resume is doing the work that never finished.

**The branch's bug is OVERLAP, not retry.** Interrupt has no blocking gate: it mints a fresh
context per epoch and the loop re-dispatches ~10ms later, while the predecessor is still
genuinely RUNNING (trace above: #63 scheduled 17:32:08.6, #53 did not complete until
17:32:10). Two live executions of the same tool at once. `checkPriorTerminalResult` cannot
stop this and never could — at re-dispatch time the row is `EXECUTING`, not terminal, which
is exactly the case it is required to let through (worker-crash retry).

**This IS the requirement violation.** Pause and interrupt currently differ in more than
scope: pause blocks before re-dispatch, interrupt does not. The fix derived from main is to
give interrupt the SAME gate pause has, scoped to one thread — the re-dispatch must wait for
the interrupted step to settle, exactly as pause's re-dispatch waits for resume. `ThreadInterrupt`
has no analog of `CheckPause` today: it has `ActivityContext` (cancel) and `Epoch`, but nothing
that blocks. That missing scoped analog is the whole defect.

Do NOT "fix" this by deleting `checkPriorTerminalResult` — it is still correct and load-bearing
for the resume path now that the wait is gone. It is just not a substitute for the gate.

## RECOMMENDATION — wait for the TOOL to settle, not for the ACTIVITY to return

The dilemma looks like "correctness (wait) vs latency (~3s)". It is a false choice, because
the 3s was never the cost of waiting — it was the cost of waiting on the WRONG THING.

`WaitForCancellation` waits for the **Temporal activity** to return, and that return can only
reach the workflow on a heartbeat, throttled by `MaxHeartbeatThrottleInterval: 3s`
(`workersetup/setup.go:135`). The 3s is the heartbeat, not the tool. Re-enabling the flag
buys back correctness at exactly the old price. **Do not re-enable it.**

But the thing we actually need to know is much smaller and much faster: *has the tool stopped?*
That answer is already durable and already local — `tool_calls.status` reaching a terminal
value, written from inside the activity on a context detached from cancellation
(`detachedForTerminalWrite`). It does not ride the heartbeat. The daemon kill path is already
verified immediate (`SendToolExecutionCancel` → `cancelToolExecution` → process group), so the
row goes terminal in roughly the time it takes to kill a process and write one row — ms, not
seconds.

**So: gate the re-dispatch on the tool row going terminal, not on the activity returning.**
Give `ThreadInterrupt` the blocking analog of `CheckPause` it is missing — the interrupt
equivalent of "block until resume" is "block until the interrupted call settles" — implemented
as a bounded `workflow.Await` on a cheap query of the interrupted call's status, with a
short timeout (~2s) that falls through to re-dispatch so a lost daemon can never park a thread
forever. Latency in the normal case is the settle time (ms). This keeps pause and interrupt on
ONE mechanism differing only in scope, which is the requirement: pause blocks the whole chat
until resume, interrupt blocks one thread until that thread's work settles.

### ORDERING BUG found while assessing this — fix regardless of the above

`InterruptThread` (`interrupt.go`) does this, in this order:
1. `signalThreadInterrupt` — cancels the activity context, which frees the workflow to
   re-dispatch immediately.
2. `inFlightToolCallsForThread` — a DB read.
3. `cancelToolCalls` — the daemon push that actually kills the tool.

**The re-dispatch is unblocked at step 1, but the tool is not killed until step 3.** That is
the ~10ms window in the trace, and it means the successor can start while the predecessor's
process is provably still alive — we signal "stop waiting" before we say "stop running".
Reversing it (kill the tools, then signal the workflow) shrinks the overlap window on its own
and is correct independently of the gate. `cancelToolCalls` is fast and the daemon push is
fire-and-forget (`tools_daemon.go:2114` — a channel send; NATS variant is `PublishMsg`), so
this costs no meaningful latency.

Note the push is fire-and-forget on BOTH transports: it is not an acknowledgement that the
process died. That is precisely why the settle gate keys on the durable `tool_calls` row
rather than on the push returning.

## DONE (fast-cancel round) — 2026-08-17

Root cause of the 1-3s stop, verified from Temporal Go SDK source (v1.37.0 AND
v1.47.0, byte-identical): **cancel latency == `MaxHeartbeatThrottleInterval`.**
Temporal delivers cancellation to a running activity ONLY in a heartbeat
RESPONSE, and `temporalInvoker.Heartbeat` unconditionally swallows any heartbeat
issued inside an open batching window (stores details, returns nil, no RPC, no
cancel check). Heartbeating MORE OFTEN CANNOT HELP — we already tick at 500ms
and the ticks were being coalesced into a 3s window.

The unlock: the throttle doubles as the heartbeat RPC deadline, BUT floors at
`minRPCTimeout = 1s`. So a sub-1s throttle does NOT tighten the deadline — the
stated reason for having raised it 500ms→3s never held.

1. **Throttle 3s → 500ms** (`workersetup/setup.go`, now the named const
   `maxHeartbeatThrottleInterval` + regression test `setup_test.go` so a silent
   raise fails in CI). This is the actual latency fix.
   `activityHeartbeatTimeout` deliberately KEPT at 30s: it governs DEAD-WORKER
   detection, not cancellation. Re-deriving it as "ten missed beats" at 500ms
   would give 5s — tight enough that a GC pause or `go build` could declare a
   LIVE worker dead and re-dispatch its activity, which for non-idempotent tools
   is the duplicate-execution failure this whole spec exists to prevent. The two
   values are now correctly decoupled.
2. **Ordering fixed — tools are killed BEFORE the workflow is freed.** Both
   verbs previously signalled the workflow first (freeing re-dispatch), then did
   a DB read, then pushed the daemon cancel — so a successor could start while
   the predecessor's process was provably still alive. Now: ownership checks →
   read in-flight calls → push daemon cancels → signal. Applied identically to
   `InterruptThread` and `PauseChat`, so the two differ ONLY in scope.
   A failed daemon push still never blocks the signal (`UndeliverableToolCalls`
   reporting preserved). Pinned red→green by
   `TestInterruptThread_CancelsToolsBeforeSignalingWorkflow` and
   `TestPauseChat_CancelsToolsBeforeSignalingWorkflow`.
3. **`shell.GetCancelSignal()` DELETED — it was dead code.** A package-level
   in-memory singleton whose every writer was in the API server and every reader
   in the worker. Two processes, two maps; `IsCancelled` in the worker could only
   ever return false. Distributed is the only supported mode, so there was no
   configuration in which it worked. Gone with it: the `SetCancelled` method on
   `threads.ToolCanceler` and the false "authoritative even when the daemon is
   unreachable" comment.
4. **SDK v1.37.0 → v1.47.0** (+ `go.temporal.io/api` v1.63.4). Zero source
   changes required. All 6 replay fixtures pass on a forced uncached run, so
   determinism holds across 10 minor versions. Does NOT change cancel latency —
   done on its own merits.

## DONE (round 2) — the cancel is now DURABLE AT THE MOMENT IT IS ASKED FOR

This is the fix that ties the rest together, and it restores (in a form that
actually works cross-process) what main did with `shell.GetCancelSignal()`.

**5. `cancelToolCalls` writes the terminal Cancelled row + result BEFORE the
daemon push** (`internal/threads/interrupt.go`, `recordToolCallCancelled`).
Previously it wrote NOTHING durable — its only effect was the daemon push — so
the Cancelled row appeared whenever the activity happened to unwind. Two
consequences, both observed live:
   - The user watched a tool spin long after stopping it, and a reload still
     showed it running. The cancel is immediate; the resume that lets the
     activity notice is not, and under pause may never come.
   - **The re-entry hole.** `checkPriorTerminalResult` only declines a call whose
     row is TERMINAL. At re-dispatch the row still said EXECUTING, so the guard
     let it through and the tool ran again — chat e5dcfc1f: an interrupted
     `spawn_status(wait)` restarted and blocked the root thread for another
     22.6s (23:16:44.514 → 23:17:07.110), which is why a queued message looked
     like it "never cleared". Writing Cancelled (terminal) at interrupt time is
     what makes the existing guard fire.
   The DB is also the only carrier that crosses the API-server/worker split that
   defeated the old in-memory signal.
   Tests: `TestInterruptThread_RecordsCancellationDurablyImmediately` (red→green
   proven by disabling the write), `..._DoesNotRewriteAlreadyFinishedCalls`,
   `..._DurableCancelIsScopedToTheThread`.

**6. Activity-retry guard** (`isActivityRetry`, `execute_tools.go`). The one
re-entry `checkPriorTerminalResult` structurally CANNOT catch: a worker that
died mid-tool never wrote a terminal row, so the call is still EXECUTING —
deliberately not terminal, since that is also what a healthy call looks like.
Temporal re-delivers the same task with `Attempt` incremented (`MaximumAttempts:
5`). Now refused, returning `InterruptedToolResultContent` on a SUCCESSFUL
activity rather than an error: failing would burn the remaining attempts and
kill the step, when the honest outcome is "the loop advances and the model is
told the tool was interrupted". `ExecuteRunStep` has done this for shell
commands since long before (`run_step.go:105`); this generalizes it.
   NOTE: a loop re-dispatch is NOT a retry — it is a new activity at attempt 1,
   handled by the durable row. And a NEW `tool_call_id` from a later LLM turn
   runs normally: the model choosing to retry is legitimate.

**7. The live event now names the same outcome as the durable row**
(`toolStatusEvent`). `emitToolStatus(..., "completed")` was issued
unconditionally BEFORE the status was computed, while the row was then written
`Failed` for an error result. The UI reads the live channel while open and the
row on reload, so the same tool rendered green then orange — observed on chat
e5dcfc1f. Both are now derived from one value.

### The three gaps from #3 — TWO CLOSED, one remains

- ✅ **`TestInterrupt_CancelWhilePendingNeverDispatches` — CLOSED and un-skipped.**
  A PENDING call is in-flight, so #5 records it Cancelled, and the guard now
  declines the dispatch. Verified through the real activity: executor execution
  count is 0.
- ✅ **Post-completion cancel marking** — #5 writes the Cancelled row directly,
  so it no longer depends on the activity noticing.
- ⚠️ **`TestInterrupt_CancelOneToolLeavesSiblingResultIntact`** — still skipped.
  Selective single-tool cancellation with a sibling's output intact needs the
  per-call distinction the shared activity context cannot make.

### Superseded by the above

The paragraph below was written when the protection was genuinely absent.

Deleting the singleton removed behavior that was already inert in production.
The tests are skipped with the reason recorded rather than deleted:
- `TestInterrupt_CancelWhilePendingNeverDispatches` — **nothing now stops a
  PENDING tool call from dispatching after an interrupt is recorded for it.**
  `checkPriorTerminalResult` covers only calls that already reached a TERMINAL
  status; a PENDING row has no terminal status, so it is let through. This is
  the most significant gap and wants a real cross-process mechanism.
- `TestInterrupt_CancelOneToolLeavesSiblingResultIntact` — no selective
  single-tool cancellation that leaves a sibling's output intact.
- `TestInterrupt_CancelAfterCompletionDoesNotDiscardRealOutput` — post-completion
  cancel marking is gone. Low severity: the call already produced its correct
  result.

These were ALREADY broken in production (the flag never crossed the process
boundary); the deletion makes that visible instead of implied-by-code-that-
cannot-run.

## Requirement

Pause and interrupt = SAME semantics. Only diff: pause = whole chat, interrupt = one thread.
Stop must be IMMEDIATE (~10ms, like main). No new persisted state.

## How main works (it works — copy it)

- NO `WaitForCancellation`. Future resolves at cancel-request = 9ms. Immediate.
- Safety = `checkPause` BLOCKS (`pc.requested=true`). Re-dispatch can't fire till resume.
- `getRawOutput` = 4 lines. Never reads cancelled activity's return. Nothing to wait for.
- Tool rows survive cancel via `detachedForTerminalWrite` (`context.WithoutCancel`) —
  written from INSIDE activity, keyed on tool_call_id (stable id from LLM output).
- Re-entry after resume: tools short-circuit on `ctx.Err()` (`execute_tools.go:496`) →
  "cancelled" result. Missing results repaired at history read (`call_llm.go:2575`,
  `InterruptedToolResultContent`) + `CleanupActivity`. Tools NOT re-run (not idempotent).
- main has NO interrupt at all. `chat_interrupt.go` is branch-only (65ce8cb7). "Interrupt
  and send now" on main = pause+send.

## What branch changed → 4 bugs

1. **Latency** — `WaitForCancellation: true` added → workflow waits for activity return →
   arrives only via heartbeat (throttle 3s) → pause regressed 9ms → 1-3s. Measured:
   pause 1.05s, interrupt 1.97s / 2.81s.
2. **Interrupt livelock** — interrupt never sets `pc.requested`, so `checkPause` returns
   instantly; `ThreadInterrupt.ActivityContext` mints FRESH `WithCancel` per epoch → re-entered
   step sees `ctx.Err()==nil` → blocking tool restarts. Chat b7cd65c6: same execute_tools
   re-dispatched 9× (349,763,769,780,852,863,902,933,1055), all `spawn_status(wait:true)`.
   Loop never reached call_llm → mailbox never drained → 5 msgs stuck status=1.
3. **PENDING skipped** — `interrupt.go:225` `if call.Status != Executing { continue }`.
   spawn_status was PENDING 22:37:35→22:41:00, so 6 interrupts cancelled nothing
   (`cancelledToolCalls=0`). Also: `IsCancelled` only checked at `execute_tools.go:617`
   AFTER run; no tool reads it.
4. **Display** — pause emits `stream_finalized` for a msg never persisted (098853e9 on
   13400985) → UI keeps stream blocks → tool renders "executing" forever
   (`ToolExecution.tsx:392`). Frontend-only; durable rows were correct.

## Tried, FAILED — do not repeat

- **Remove `WaitForCancellation` alone.** SHIPPED LIVE, BROKE CHAT b7cd65c6. Bare
  `ErrCanceled` (no details) → no settle → re-dispatch into fresh ctx → livelock. Reverted.
- **Detached partial-persist in call_llm** (mirror execute_tools). Tried, reverted.
  PROVEN IMPOSSIBLE today: preallocated msg id is FRESH per dispatch
  (`SideEffect` is positional, not memoized — `preallocated_id_redispatch_test.go`),
  `SaveMessage.checkExistingMessage` keys on ActivityID NOT MessageID, `CreateMessage` is
  plain INSERT no ON CONFLICT → duplicate assistant msg per interrupt.
  **call_llm has no stable identity. That's the missing primitive.**
- **Deterministic msg id from (wf,step,iter).** Rejected: re-dispatch should be a FRESH
  turn; id reuse collides with abandoned attempt.
- **CORRECTION — I was wrong to reject this one.** I earlier dismissed a sub-agent's finding
  that `MarkAgentMessagesDelivered` lacks a `status = 1` guard, saying the mailbox SQL was
  fully guarded. Re-checked: `agent_messages.sql:16-21` genuinely has NO guard, while every
  sibling (lines 13, 25, 34, 52, 76) does. The agent was right. It was NOT the cause of the
  b7cd65c6 stranding (those rows were never claimed — starvation, not a race), but it IS a
  real latent bug and it BLOCKS removing the wait, because the drain is then not safe
  against a concurrent duplicate:
  `Execute` lists rows OUTSIDE any tx (`drain_agent_messages.go:64`), the mark-delivered
  UPDATE is unguarded, and the drain's `SaveMessage` calls pass NO ActivityID (so
  `checkExistingMessage` never runs → unconditional INSERT). Two overlapping drains both
  insert envelope + bodies = duplicate transcript messages.
- Claims that were WRONG (mine): "3s throttle is a regression" (identical on main);
  "main has no interrupt, revert to in-process signal" (main has no interrupt at all);
  "3 concurrent activities = overlap bug" (they were 3 different spawn threads — normal);
  "mailbox SQL missing status=1 guard" (claim/count/cancel/sweep all guarded; rows were
  never claimed — starvation not race).

## Key facts (verified)

- SDK: cancel reaches worker ONLY via heartbeat response. No per-activity throttle
  override. `MaxHeartbeatThrottleInterval: 3s` (workersetup:135), `activityHeartbeatTimeout`
  30s sized against it — move together. Temporal gives NO overlap prevention; fencing is
  app's job.
- `WaitForCancellation` does 2 things: (a) serializes, (b) makes CanceledError carry
  DETAILS → `getRawOutput` settle path → loop ADVANCES. Without it interrupt = infinite
  retry of same step.
- Only `CallLLM` builds `NewCanceledError(details)`. `ExecuteTools` NEVER does — always
  returns `(output,nil)`. So interrupted tool step can ONLY re-dispatch.
- Healthy run (13400985, flag on): 2 `ACTIVITY_TASK_CANCELED`, both w/ details, loop
  advanced iter1→iter2. Broken run: 0 cancel events, 9× same iteration.
- `spawn_status(wait:true)` DOES honour cancel (`select rctx.Done()`). Tool is fine.

## Next candidates

- **A. Per-activity flag split** — keep `WaitForCancellation` for call_llm only, drop for
  execute_tools (already survives via detached writes). Fixes tool-step immediacy =
  the spawn_status case that stranded msgs. Small, targeted. START HERE.
- **B. Stable assistant-msg identity** — new idempotency key replacing ActivityID; then
  call_llm persists detached, flag comes off entirely, pause back to 9ms. Principled, big.
- **C. Fix #3** — widen guard to PENDING+EXECUTING, check cancel signal before dispatch.
  2 lines. Independent.
- **D. Fix #4** — frontend settles orphaned stream blocks. Independent.

## Tests pinning this

- `internal/workflow/runtime/interrupt_livelock_test.go` — bare cancel can't settle →
  re-dispatch; cancel w/ details settles → advances.
- `internal/workflow/runtime/preallocated_id_redispatch_test.go` — msg id fresh per dispatch.

## DONE

- **C — interrupt now cancels PENDING tool calls.** `interrupt.go`:
  `executingToolCallsForThread` → `inFlightToolCallsForThread`, guard widened to
  Pending+Executing. `execute_tools.go`: cancel-signal check added AFTER the PENDING upsert
  and BEFORE dispatch — cancelled-while-pending now writes terminal Cancelled + result and
  never runs the tool. Tests: `TestInterruptThread_CancelsPendingToolCallsOnThread`,
  `TestInterrupt_CancelWhilePendingNeverDispatches`. Both verified red→green.
- **D — orphaned stream display.** Most was already fixed in-tree
  (`isAbortedFinalizeReason`/`settleCancelledStreamBlocks`). Remaining hole: when deltas and
  the aborting marker arrive in the SAME batch, "THE DROP RULE" (`chatStore.ts:2051`) filters
  the deltas so no placeholder exists for the settle pass to walk → tool stuck "executing"
  via `toolCallStates`. Fixed w/ a second settle pass + `toolStatusSurvivesStreamAbort()`
  (also stops a `backgrounded` tool being repainted cancelled).
  Tests: `chatStore.orphanedStreamSettle.test.ts` (6). Web suite 1882 pass.
- **Partial-output-on-cancel bug (found while verifying C).** `streamLoop` used one select
  with both `<-streamCtx.Done()` and `<-eventChan`; Go picks RANDOMLY among ready cases, so
  cancel could win while text was still buffered → partial dropped → interrupted turn settled
  with empty `response_text`. Fixed: drain buffered events first (non-blocking select), then
  check cancel. Pinned by `TestCallLLM_CancelledStreamReturnsPartialOutput`.
  NOTE: I had deleted `NewCanceledError(details)` + `callLLMOutputCancellationDetails` from
  `call_llm.go` during the failed "remove the flag" attempt; restored. That details payload
  is the ONLY channel a cancelled activity has, and it requires `WaitForCancellation` — the
  two are one mechanism, do not remove either alone.

## Done (cont.)

- **A — per-activity flag split.** `WaitForCancellation` now only on `call_llm`
  (`waitsForCancellation(node)` in `step_executor.go`). execute_tools/save_message/etc stop
  IMMEDIATELY at cancel-request (~10ms) instead of waiting on the 3s heartbeat, because they
  persist their own work durably (`detachedForTerminalWrite`). call_llm keeps the wait — its
  partial only escapes as cancellation details, and abandoning it caused the b7cd65c6
  livelock. Pinned by `wait_for_cancellation_test.go`. Runtime + replay fixtures green.
- **Daemon cancel VERIFIED (was unverified).** `SendToolExecutionCancel` really does kill the
  process: `runtime.go:627` → `cancelToolExecution` (:1237) → `cancel()` on the registered
  `context.CancelFunc` (:1007) → `execCtx` → `local.go:734` → `exec.CommandContext`
  (`local_unix.go:12`, with `Setpgid`). Works over BOTH gRPC and NATS. Cancel after finish is
  a safe no-op (entry already unregistered). So the daemon push IS the immediate path; the
  1.77s was purely the heartbeat.
- **#2 LIVELOCK — FIXED.** `execute_tools.go`'s `executeSingleTool` now checks, before ANY
  dispatch work, whether `toolCallID` already has a durable row whose status is terminal
  (`core.ToolCallStatus.IsTerminal` — Completed/Failed/Cancelled; Backgrounded deliberately
  excluded, it is still running). If so, it returns the recorded `tool_call_results` content
  (or `InterruptedToolResultContent` if the row is terminal but the result never landed)
  instead of executing — `checkPriorTerminalResult`. This fixes the livelock at the layer
  that owns tool non-idempotency, for BOTH pause and interrupt re-dispatch, not just the
  interrupt path: a re-entered step now sees the SAME outcome regardless of whether its
  context happens to still be cancelled or was minted fresh. Added `GetToolCallResult`
  (repo + store + sqlc query) since no single-row result read existed. Also fixed the
  `workflow.go:1799` log line, which said "(pause)" unconditionally; it now reports
  `cause=pause` or `cause=interrupt` via `threadInterruptedSince`.
  Tests: `execute_tools_terminal_idempotency_test.go` (7, red→green against the unfixed
  code), plus the long-parked `TestExecuteToolsActivity_NoReExecutionOnRetry` un-skipped and
  now passing for real (it had been documenting this exact gap since before this spec).

## Done (cont. 2)

- **E — pause now pushes the daemon tool-cancel.** Shared loop extracted from `interrupt.go`
  into `cancelToolCalls()`; added `inFlightToolCallsForChat` (chat-scoped sibling of the
  thread-scoped one); new `internal/threads/pause_tool_cancel.go` `CancelChatToolCalls()`;
  wired into `PauseChat` after `runs.Pause` succeeds, best-effort/non-fatal. Pause and
  interrupt now share ONE cancel mechanism, differing only in scope. 6 new tests.
- **F — abandoned tools now killed on the daemon (user's idea).** The daemon runs each exec
  under `context.WithCancel(context.Background())` — DECOUPLED from the request — and
  `ExecuteTools` never pushed a cancel when its own ctx died. So a paused/interrupted tool
  was marked cancelled server-side while the user's `bash` RAN TO COMPLETION on their
  machine. Fixed with a defer in `RemoteExecutor.executeOnDaemon`: if `ctx.Err() != nil` on
  the way out, push `SendToolExecutionCancel` (detached ctx, 5s budget, keyed on ToolCallID
  since the daemon registers under both ids). Tests in `remote_executor_abandon_test.go`,
  verified red→green.

## Done (cont. 3)

- **#2 LIVELOCK FIXED.** Fixed at the right layer: `executeSingleTool` now calls
  `checkPriorTerminalResult` BEFORE any dispatch — a tool_call_id that already reached a
  terminal status (Completed/Failed/Cancelled; Backgrounded excluded, still running) returns
  its recorded result instead of re-executing. Independent of `ctx.Err()`, so it closes the
  livelock for BOTH interrupt and pause re-dispatch. Added read-only `GetToolCallResult`
  (no schema change, no new persisted state — verified). Also fixed the `workflow.go` log
  that said "(pause)" for interrupts too, which misled this investigation for hours.
  7 tests in `execute_tools_terminal_idempotency_test.go`, red→green (3 cases re-executed
  the tool before the fix). Un-skipped `TestExecuteToolsActivity_NoReExecutionOnRetry`.

## Done (cont. 4)

- **G — mailbox drain is now atomic + idempotent** (prerequisite for B, and a real latent
  bug on its own). Three parts:
  1. `MarkAgentMessagesDelivered` is now `AND status = 1 ... RETURNING id` — it CLAIMS, and
     reports what it actually claimed. Was unguarded (the one outlier in a file where every
     sibling is guarded).
  2. The drain now claims **BEFORE** writing anything (`drain_agent_messages.go`), so a
     loser writes NOTHING. It used to mark delivered LAST, by which point both racers had
     already inserted their envelope + bodies = duplicate transcript messages.
  3. New `SetAgentMessagesDeliveredMessageID` backfills the envelope pointer after the
     envelope exists. The claim leaves `delivered_message_id` NULL — it is a FK into
     `messages`, and the envelope does not exist yet. (First attempt used a placeholder
     string and hit `agent_messages_delivered_message_id_fkey`; the column is nullable, so
     NULL is the right claim value.)
  Tests: `agent_messages_concurrent_drain_test.go` — two goroutines released together on one
  batch; exactly one wins, loser gets zero. Red proven by removing the guard: BOTH claimed
  the same rows. Also pins retry-safety (already-delivered claims nothing).

## Done (cont. 5)

- **G FIX — message-loss bug found in review, in G itself.** The lost-race path did
  `return nil`, and `RunTx` COMMITS on nil (`repo.go:323-339`) — so the rows already claimed
  in that transaction committed as status=2 with NO message written. Every read path filters
  status=1, and there is no sweeper for `status=2 AND delivered_message_id IS NULL`, so those
  messages were gone PERMANENTLY with no error surfaced. Exactly the failure G existed to
  prevent. Fixed: return sentinel `errDrainBatchTaken` so the claim ROLLS BACK; caller maps
  it to "nothing delivered". Partial claims are reachable not just from a competing drain but
  from "Send now" and cancel, which DELETE queued rows.
- **B part 1 DONE** — `callLLMIdempotencyKey(workflowID, stepID, loopNodeID, loopIteration)`
  set in `buildRuntimeContext`, gated on `streamFinalizedVersionGate`, length-prefixed like
  `injectIdempotencyKey`, "noloop" vs "iter:N" so a top-level node can't collide with a
  loop's iteration 0. Uses `model.NodeTypeCallLLM` (a raw "call_llm" literal is caught by
  `TestNoRawNodeTypeLiteralsInProductionFiles`).
- **B part 2 DONE** — `persistInterruptedTurn` in `call_llm.go` persists the partial on
  `context.WithoutCancel` + 5s, keyed on the stable idempotency key, so a re-dispatch
  converges on one row. Runs ALONGSIDE the existing cancellation-details return (not
  replacing it) — the system behaves as before, just with the partial also durable.

## Done (cont. 6) — THE WAIT IS GONE

- **B part 3 DONE.** `WaitForCancellation` removed entirely from `activityOptions`; every
  step now stops at cancel-request (~10ms) instead of waiting out the 3s heartbeat. Deleted
  with it: `waitsForCancellation`, `NewCanceledError(callLLMOutputCancellationDetails(...))`
  in call_llm, `callLLMOutputCancellationDetails`, and the `HasDetails()` harvest in
  `getRawOutput`. `threadInterruptedSince` KEPT — still used to log pause vs interrupt.
  Tests rewritten to the new contract:
  `TestActivityOptions_NeverWaitForCancellation` (asserts the flag on call_llm /
  execute_tools / save_message directly, so re-adding it fails here not in a user's chat),
  `TestCallLLM_CancelledStreamPersistsPartialTurn` (partial is DURABLE, read back from the
  DB, instead of returned as cancellation details),
  `TestStepExecutor_InterruptedCallLLMSurfacesCancellation`.
  Verified: inline save_message builds its OWN rtx (`save_message.go:656`) and does NOT
  inherit the call_llm idempotency key, so the successful turn can't be mistaken for a dup.
- **F over-fire FIXED** (review finding). Was gated on ambient `ctx.Err()`, which is true for
  a tool that SUCCEEDED while a sibling was cancelled — stray cancel on a reusable tool call
  id, and a killed process group for a BACKGROUNDED tool the user asked to keep. Now gated on
  `abandoned`, set only when the dispatch itself returns an error. New test
  `TestExecuteOnDaemon_SiblingCancellationDoesNotCancelCompletedCall`.
- **E's process-local flag: NOT A BUG.** Monolith mode no longer exists, so the "flag persists
  across resume" failure is unreachable. In distributed mode the daemon push is the real
  mechanism and works; only the comment at `interrupt.go:255-258` overstates the in-process
  signal. Comment-only issue.

## Still open

- **A doc inaccuracy** (review): a comment claims `save_message` survives cancellation via
  detached writes. It has no `WithoutCancel`; it survives because the step re-runs. Code is
  right, comment's reasoning is wrong.
- **#2 behavior change, undocumented** (review): a tool-level failure writes terminal Failed
  before the activity can fail, so a later activity retry returns the recorded failure rather
  than re-running the tool. Probably correct (tools aren't idempotent) but should be stated.

## Superseded

- **B part 3 (old text)** — remove `waitsForCancellation` + `WaitForCancellation`, then the now-dead
  `NewCanceledError(callLLMOutputCancellationDetails(...))` and the `HasDetails()` harvest in
  `getRawOutput`; rewrite `TestCallLLM_CancelledStreamReturnsPartialOutput`,
  `TestStepExecutor_ThreadInterruptSettlesCallLLMAndRunsSaveMessage`,
  `wait_for_cancellation_test.go`. **Prerequisites G and B1/B2 are done**, so the drain is
  now safe under overlap and the partial survives without the wait.
- **From review, not yet fixed:**
  - **F over-fires**: guard is `ctx.Err() != nil`, but all tools in a turn share ONE activity
    ctx (`execute_tools.go:637-642`), so a sibling's cancellation makes a SUCCESSFUL tool's
    defer push a cancel. Mostly benign (daemon unregisters on completion) EXCEPT for
    backgrounded tools — `pollForBackgroundDetach` returns success and the adopted process's
    cancelFunc kills the process group. Gate on the call's own outcome + exclude
    `resp.Backgrounded`.
  - **E's in-process cancel flag is process-local**: `shell.CancelSignal` is a package
    singleton set in the API server but only read in the WORKER — different binaries in
    distributed mode. Daemon push is the load-bearing half; the "authoritative even when the
    daemon is unreachable" comment (`interrupt.go:255-258`) is false cross-process. In
    MONOLITH it's worse: the flag persists, so a PAUSED call re-dispatched on resume hits the
    PENDING check and writes terminal Cancelled without running — pause becomes interrupt,
    contradicting `specs/pause-and-resume.md`. Needs the flag cleared on resume.
  - **#2 minor**: a tool-level failure writes terminal Failed BEFORE the activity can fail,
    so a later activity retry returns the recorded failure instead of re-running. Arguably
    correct (tools aren't idempotent; `ExecuteRunStep` already refuses retry) but it IS a
    behavior change from "retry the tool" to "never retry the tool".
  - **A doc inaccuracy**: the comment claims `save_message` survives via detached writes. It
    has no `WithoutCancel` — it survives because the step re-runs. Code right, comment wrong. so the drain is safe
  under overlap and the wait can be removed. Remaining: set deterministic
  `MessageIdempotencyKey` for call_llm in `buildRuntimeContext` (components verified stable:
  `workflowID`, `node.GetId()`, `loopIteration`; mechanism already exists and is used by
  `injectIdempotencyKey`), persist call_llm's partial detached, then drop
  `waitsForCancellation` + the cancellation-details path and rewrite the 3 tests that pin the
  old contract.
  Feasibility CONFIRMED: `RuntimeContext.MessageIdempotencyKey` already exists as the
  override (`save_message.go:118`), already used by the inject path
  (`injectIdempotencyKey`, `child_workflow_init.go:151`). Key components
  (`workflowID`, `node.GetId()`, `loopIteration`) all present in `buildRuntimeContext` and
  stable across re-dispatch. Plan: set the key → persist partial detached → remove the wait.
  **Gating risk: with the wait gone, `call_llm` can overlap its re-dispatch by up to 3s.
  Must prove a concurrent duplicate `drainAgentMailbox` is safe before shipping — that is
  exactly what stranded 5 messages on b7cd65c6.**
  Original problem text: `call_llm` can't persist
  detached (id fresh per dispatch, SaveMessage keys on ActivityID, plain INSERT) so it needs
  `WaitForCancellation`, so call_llm cancel is still heartbeat-bound (~1-3s). Fixing B
  retires the flag entirely and makes call_llm ~10ms like everything else.

## Env

- **DB tests need `DATABASE_URL`** or they silently skip and report `ok`.
  Use the per-worktree dev DB (`scripts/dev.sh` provisions it); repo compose Postgres is
  `localhost:5433`. Do NOT point at 5434 (control-plane's, holds real data).
- App DB: `PGPASSWORD=postgres psql -h localhost -p 5434 -U postgres -d reliant`
- Temporal: `temporal --address 127.0.0.1:7233 --namespace reliant workflow show
  --workflow-id X --run-id Y --output json` → top key `.events`
- Air NOT running; user does normal builds. Source edit ≠ live until rebuild.
- ⛔ no `git stash/checkout/reset/commit` (shared worktree). No `pkill`/`forge env down`.
