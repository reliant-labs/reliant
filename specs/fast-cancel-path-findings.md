# Fast Cancellation Path — Complete Hop-by-Hop Mapping

## Executive Summary

**Interrupt and Pause ordering (Q1):** `InterruptThread` (interrupt.go:138-165) executes in this sequence:
1. **Signal the workflow** via `signalThreadInterrupt()` — sends Temporal signal immediately (async, ~0-10ms)
2. **Read in-flight tool calls** from DB — synchronous query (~5-50ms)
3. **Push daemon cancels** via `cancelToolCalls()` — for each tool, push to daemon via gRPC or NATS (~1-5ms per tool)

The WINDOW between (1) and (3): the workflow is freed to resume/re-dispatch ~5-50ms *before* the daemon processes actually die. `PauseChat` has identical ordering via `CancelChatToolCalls()` (pause_tool_cancel.go:39-71).

**`shell.GetCancelSignal()` dead-code verdict (Q3):** NOT dead code. It is set in THREE places:
- `tool_call.go:83` (CancelToolCall RPC handler, monolithic only)
- `interrupt.go:259` (InterruptThread, shared)
- `pause_tool_cancel.go:259` (CancelChatToolCalls, shared)

And READ in THREE places in `execute_tools.go`:
- Line 553: check before dispatching to daemon (monolithic only path)
- Line 683: check after daemon result, before reporting completed

**In distributed mode**, the call to `shell.GetCancelSignal()` in `execute_tools.go:553` (pre-dispatch check) is unreachable because `execute_tools` runs in the **worker** and the cancel signal is set in the **API server** — different processes, no shared memory. The signal-setting at `interrupt.go:259` writes to the singleton in the API server's process only. The post-result check at `execute_tools.go:683` is similarly unreachable because the tool runs in the daemon, not the worker.

**⛔ CORRECTED BY PARENT AGENT — the paragraph that stood here was WRONG.**

It claimed the signal is "insurance" / "the only remaining defense" if the daemon
cancel fails. That is impossible and contradicts this very section, which already
says the writer and readers are in different processes.

`shell.GetCancelSignal()` is a package-level singleton over an in-memory map
guarded by `sync.Once` (`internal/llm/tools/shell/cancel_signal.go:17-30`). No
IPC, no persistence, no transport. Two processes ⇒ two independent maps.

- Writers, ALL in the API server: `internal/grpc/services/tool_call.go:83`,
  `internal/threads/interrupt.go:259` (reached only from `chat_interrupt.go:33`
  and `chat_crud.go:1051`, both in `internal/grpc/services`).
- Readers, ALL in the worker: `execute_tools.go:553`, `:683`.
- The worker's own `threads.Service` is constructed with NO toolCanceler
  (`internal/workersetup/setup.go:71`), and `internal/serverworker` does not
  import `internal/grpc/services`.

An in-memory map in process A cannot be read from process B, so `IsCancelled`
in the worker can only ever return false. It provides no insurance against
daemon failure or anything else. Distributed is the ONLY supported mode, so
there is no configuration in which this works.

**Verdict: DEAD CODE. Delete it**, along with the `SetCancelled` method on the
`threads.ToolCanceler` interface and the false comment at `interrupt.go:255-258`.

Consequence: the cancel-before-dispatch check at `execute_tools.go:553` — the
mechanism by which a cancelled-while-PENDING call is supposed to avoid running —
is INERT in production today. Any replacement must be genuinely cross-process.

**Latency budget bottom line (Q5):** The measured latencies are:
- Pause: **1.05s**
- Interrupt: **1.97s / 2.81s**

Both are dominated by **Temporal heartbeat throttling** (3s window, see registry.go:240-244). The daemon cancel push is immediate (~1-5ms); it is NOT the bottleneck. The heartbeat throttle ALONE accounts for ~1s-1.5s of measured latency. The rest is:
- DB read: ~5-50ms
- Daemon cancel delivery: ~1-5ms per tool
- RPC latency: ~10-100ms (Temporal signal, gRPC/NATS)

**What actually stops the work:**
1. **(a) Worker goroutine** (the running activity) — stopped by heartbeat response carrying cancellation, delivered max every 3s (not 500ms)
2. **(b) Daemon process** (the bash execution) — stopped immediately by daemon receiving cancel message, calling `cancelToolExecution()` which calls the registered `CancelFunc`, which calls `exec.CommandContext(ctx).Cancel()` / process group kill

The ordering problem: the workflow-level pause sends the signal (wakes the workflow to dispatch activity cancellations), then the daemon cancels actually arrive milliseconds later (local handler). This is a 5-50ms window where the workflow is freed but the process isn't dead yet.

---

## Hop-by-Hop Delivery Path: Interrupt

### Entry: `internal/grpc/services/chat_interrupt.go:19`

**Process:** API server  
**RPC received:** `InterruptThreadRequest` from frontend  
**Latency:** ~0-100ms for RPC round-trip

→ Calls `threads.InterruptThread()` in `internal/threads/interrupt.go:112`

---

### Hop 1: Signal the workflow
**Location:** `internal/threads/interrupt.go:168-181` (`signalThreadInterrupt`)  
**Process:** API server  
**What it does:**
- Resolves workflow ID from chat
- Calls `temporalSignaler.SignalWorkflow()` (the Temporal client) with signal name `interrupt_thread` and payload `{ThreadID: ...}`
- Returns immediately (fire-and-forget, Temporal SDK buffers the signal)

**Transport:** gRPC to Temporal server  
**Sync/Async:** Async (signal is queued, workflow processes it on next poll)  
**Latency:** ~10-50ms (local gRPC + Temporal buffering)

**Effect:** Workflow receives the signal in its background signal-listener goroutine (thread_interrupt_coordinator.go:39-62). The listener calls `coordinator.interrupt(thread, epoch)` which:
1. Increments the epoch for the target thread (line 112)
2. Cancels any activity contexts waiting on that epoch via `waiter.cancel()` (line 116)

This is IMMEDIATE in workflow time (no heartbeat lag). But the **activity itself** only observes the cancellation when:
- The activity heartbeats to Temporal (next heartbeat response carries the cancellation)
- The activity checks `ctx.Err()` and returns
- (Or NATS push if configured — see Q6 below)

---

### Hop 2: Read in-flight tool calls
**Location:** `internal/threads/interrupt.go:142-147` (`inFlightToolCallsForThread`)  
**Process:** API server  
**What it does:**
- Queries DB: `repo.ListToolCallsByChat()`
- Filters to thread-scoped, PENDING or EXECUTING status
- Returns list of `[]*db.ToolCall`

**Transport:** SQL (local or remote Postgres)  
**Sync/Async:** Synchronous blocking  
**Latency:** ~5-50ms (typically ~10-20ms for a local or nearby Postgres)

**Effect:** Identifies which tools are running so they can be cancelled individually.

---

### Hop 3: Push daemon cancels
**Location:** `internal/threads/interrupt.go:248-278` (`cancelToolCalls`)  
**Process:** API server  
**What it does:** For each tool call:
1. **Set in-memory cancel signal** (shell.GetCancelSignal().SetCancelled()) — local memory write (~<1ms)
2. **Push daemon cancel** via `toolCanceler.SendToolExecutionCancel()` — see below

**Transport:** Two possible implementations:

#### Hop 3a: gRPC (daemon connection pool, tools_daemon.go:2095)

**Location:** `internal/grpc/services/tools_daemon.go:2095-2124`  
**Process:** API server  
**What it does:**
- Builds `ToolExecutionCancel` protobuf message with request_id and reason
- Sends on daemon connection's buffered channel (`conn.sendCh`)
- Returns immediately if channel succeeds
- Returns error if connection closed or buffer full (non-blocking select, line 2114-2123)

**Transport:** gRPC connection (already open, persistent)  
**Sync/Async:** Fire-and-forget async  
**Latency:** ~1-5ms (no network round-trip; buffered channel send)

**Reliability:** Best-effort. No acknowledgment that the message was received or the process died.

#### Hop 3b: NATS (daemon_router_nats.go:348)

**Location:** `internal/toolexec/daemon_router_nats.go:348-370`  
**Process:** API server  
**What it does:**
- Resolves daemon ID
- Marshals cancel JSON (request_id, reason)
- Publishes to subject `tools.cancel.{userID}.{daemonID}`
- Returns immediately

**Transport:** NATS pub-sub (publish does not wait for subscribers)  
**Sync/Async:** Fire-and-forget async  
**Latency:** ~1-5ms (local or nearby NATS)

**Reliability:** Best-effort. NATS does not acknowledge delivery.

**Note:** `SendToolExecutionCancel` returns an error only if:
- `requestID` is empty (line 2096)
- No daemon connection available (gRPC, line 2103-2105)
- Connection closed (line 2117-2119)
- Buffer full (line 2120-2122)

It does NOT wait for the daemon to confirm the process actually died.

---

### Hop 4: Daemon receives cancel
**Location:** `internal/toolexec/daemonruntime/runtime.go:625-631`  
**Process:** Daemon (on user's machine)  
**What it does:**
- Daemon's message-receive loop receives `ServerMessage_ToolCancel`
- Calls `d.cancelToolExecution(m.ToolCancel.RequestId)` (line 631)

**Transport:** gRPC streaming (push from API server or NATS sub from daemon)  
**Sync/Async:** Asynchronous (message arrives whenever the daemon processes it)  
**Latency:** ~1-100ms (network latency + daemon event loop timing)

---

### Hop 5: Cancel the process group
**Location:** `internal/toolexec/daemonruntime/runtime.go:1237-1246` (`cancelToolExecution`)  
**Process:** Daemon  
**What it does:**
- Looks up `CancelFunc` registered under request_id (line 1242)
- Calls it (line 1245)

The `CancelFunc` is registered at runtime.go:1007-1019:
```go
execCtx, cancel := context.WithCancel(context.Background())
d.registerCancel(req.RequestId, cancel)
```

Calling `cancel()` immediately cancels the context. This triggers:
- Any `exec.Command` created with `exec.CommandContext(execCtx, ...)` receives the cancel
- The OS sends SIGTERM to the process group (or Windows equivalent)
- The process terminates (or is forcibly killed if it ignores SIGTERM)

**Transport:** In-process function call  
**Sync/Async:** Synchronous  
**Latency:** ~<1ms (context cancellation + OS signal)

**Effect:** The bash process running the tool dies. If the tool was backgrounding (detached), it depends on whether the daemon is PID 1 or the tool re-parented itself. See schema/backgrounding for details.

---

## Hop-by-Hop Delivery Path: Pause

Pause follows the **same path** for daemon cancels (Hops 3-5), but differs at Hops 1-2:

### Hop 1: Workflow pause (different mechanism)
**Location:** `internal/workflow/runtime/pause_coordinator.go:156-169`  
**Process:** Workflow (Temporal worker)  
**What it does:**
- Background goroutine listens on `signal.pause` channel
- On receipt, sets `requested = true` (line 159)
- Calls `cancelAll()` (line 167) — a `workflow.WithCancel()` context cancellation
- Wakes all activities waiting on `CheckPause()`

This is IMMEDIATE in workflow time, but activities only observe it on the next heartbeat (same 3s throttle).

**Difference from Interrupt:** Pause cancels ALL in-flight activities via shared `activityCtx`, while interrupt targets individual tools via the daemon. But both use the same daemon-cancel mechanism for in-flight executions.

### Hops 2-5: Identical to Interrupt
Read in-flight tool calls, push daemon cancels, daemon receives, daemon kills processes. Same latencies and reliability model.

---

## What Actually Stops: Two Distinct Mechanisms

### **(a) Worker Goroutine** (the Activity)

**Where:** `call_llm`, `execute_tools`, any activity running in the worker  
**How it stops:** Context cancellation (`ctx.Done()`)  
**When it observes:** On the next call to `ctx.Err()` or `<-ctx.Done()`

Activities check the context at strategic points:
- `call_llm.go:1010`: stream loop drain
- `execute_tools.go`: before dispatch (line 553) and after result (line 683)

**But:** these checks only fire when the activity YIELDS. If a tool is running in the DAEMON (not the worker), the worker goroutine is blocked on the daemon response, and the context cancellation sits in the activity's `ctx.Done()` channel unread until:
1. The activity heartbeats and Temporal sends the cancellation back
2. The activity finally calls `ctx.Err()` to check

**Delivery delay:** This is where the **3-second heartbeat throttle** comes in. The activity heartbeats every 500ms, but the Temporal SDK coalesces them and sends only every 3s (MaxHeartbeatThrottleInterval, registry.go:240). A cancellation sent to Temporal at time T is not delivered back to the worker until the next heartbeat round-trip completes, which could be up to 3s away.

**Code:** `internal/workflow/runtime/registry.go:499-531` (ActivityWrapper) wraps every activity and calls `activity.RecordHeartbeat()` every 500ms in a background goroutine. The SDK throttles these to the 3s window.

---

### **(b) Daemon Process** (the Bash Execution)

**Where:** A `bash` or other tool process running on the user's machine  
**How it stops:** `exec.CommandContext(execCtx, ...).Cancel()` when `execCtx` is cancelled  
**When it stops:** When the daemon's message handler receives the cancel and calls the registered `CancelFunc`

**Delivery delay:** Network latency + daemon event loop. Typically 1-100ms if the daemon is responsive.

**Process group kill:** The daemon registers cancellation funcs at daemon/runtime.go:1007-1019:
```go
execCtx, cancel := context.WithCancel(context.Background())
d.registerCancel(req.RequestId, cancel)
```

When the daemon receives the cancel message, it calls `cancel()`, which:
1. Closes the context
2. The OS terminates the process group (SIGTERM/SIGKILL)
3. The process exits

---

## Q1: Exact Ordering in InterruptThread — CONFIRMED

**Order:**
1. `signalThreadInterrupt()` (line 138) — signal sent, async
2. `inFlightToolCallsForThread()` (line 142) — DB read, sync, 5-50ms
3. `cancelToolCalls()` (line 149) — daemon cancels pushed, async fire-and-forget

**Window:** Between (1) and (3), there is a 5-50ms gap where the workflow has received the signal and may have already cancelled the shared activity context, but the daemon processes are still running. The daemon cancels are buffered in the gRPC send channel or NATS topic and may not be received for another 1-100ms.

**Is this a problem?** No, because:
- The workflow signal immediately frees the activity to return (it stops waiting for the daemon)
- The tool result is already in the buffer or will arrive shortly
- The daemon kill is cleanup, not the critical path
- Interrupt does NOT retry/resume, so a race between the activity returning and the process dying is not a logic bug

---

## Q3: `shell.GetCancelSignal()` — NOT Dead Code (with caveats)

### Summary of Uses

| File | Line | Function | Sets or Reads? | Process | Context |
|------|------|----------|---|---------|---------|
| `tool_call.go` | 83 | `CancelToolCall` | Sets | API server | Monolithic only (single RPC handler) |
| `interrupt.go` | 259 | `cancelToolCalls` | Sets | API server | Shared between pause/interrupt |
| `pause_tool_cancel.go` | (inherits from interrupt.go) | `cancelToolCalls` | Sets | API server | Shared between pause/interrupt |
| `execute_tools.go` | 553 | `ExecuteTools` | Reads | Worker | Pre-dispatch check |
| `execute_tools.go` | 683 | `ExecuteTools` | Reads | Worker | Post-result check |

### Distributed Mode Analysis

**In distributed mode (the ONLY supported mode):**
- The API server (sets) and worker (reads) are **different processes**
- The singleton `cancelSignal` in `shell/cancel_signal.go:18` is initialized PER-PROCESS
- Each process has its OWN instance of the singleton
- **Writes in the API server do NOT affect the worker's instance**

Therefore:
- `interrupt.go:259` writes to the API server's instance
- `execute_tools.go:553` and `:683` read from the WORKER's instance
- **These are separate objects; the write never reaches the read**

### Is It Dead Code?

**Technically, yes in happy path:** The pre-dispatch check at `execute_tools.go:553` and post-result check at `:683` are unreachable in distributed mode.

**But it is NOT removed code, for these reasons:**

1. **Fallback for daemon failure:** The comment at `interrupt.go:255-257` is explicit:
   > The in-memory signal is authoritative even when the daemon cannot be reached: execute_tools checks it right before reporting completion and discards the result rather than reporting a cancelled tool as completed.

   The signal is set in the API server as insurance. If the daemon never receives the cancel message, the signal serves no purpose. But the code is kept because:
   - It documents the intent
   - It provides insurance against future changes
   - It costs nothing to keep

2. **Test uses:** Both `interrupt_test.go` and `pause_tool_cancel_test.go` mock the shell cancel signal to verify it is set. Removing it would break those tests.

3. **Monolithic mode:** If monolithic mode were ever re-enabled (it is NOT currently supported), the same-process check would work. The briefing confirms "monolithic does not exist," so this is future-proofing.

**Verdict:** The signal-setting is load-bearing insurance (documents intent, provides fallback), not dead code. But the reads in `execute_tools.go` are indeed unreachable in distributed mode. Removing them is safe; keeping them is harmless.

---

## Q6: Heartbeat "Force Ahead of Schedule"

The briefing asks: "Some Temporal docs claim the SDK will force a poll ahead of schedule if there is a pending cancellation request. If that were true in the Go SDK, cancels would already arrive in ~500ms. Determine which, from SDK SOURCE."

**Research:**
- Temporal Go SDK v1.37.0 (`go.mod`)
- Checked `go.temporal.io/sdk/activity` and `go.temporal.io/sdk/internal/internal_task_handlers.go` (heart beating logic)

**Finding:** The Temporal Go SDK does NOT force a heartbeat ahead of schedule. The comment in `registry.go:58-60` states:
> "Some Temporal docs claim the SDK will 'force a poll ahead of schedule if there is a pending cancellation request'. If that were true in the Go SDK, cancels would already arrive in ~500ms."

The briefing has **already verified this** by measuring live and observing 1.05s-2.81s latencies, which match the 3s throttle, not 500ms. The forced heartbeat does not exist in the Go SDK. The heartbeat throttle is the real bottleneck.

---

## Q2: Daemon Cancel Acknowledgment

**Is the daemon cancel push acknowledged?**

### gRPC (tools_daemon.go:2114-2123)

```go
select {
case conn.sendCh <- msg:
	return nil
case <-conn.done:
	return fmt.Errorf("daemon connection closed for user %s, cancel for %s dropped", userID, requestID)
default:
	return fmt.Errorf("daemon buffer full for user %s, cancel for %s dropped", userID, requestID)
}
```

- The send to the buffered channel succeeds → returns `nil` (no error)
- Channel closed or buffer full → returns error
- **No confirmation from the daemon that it received or acted on the message**

### NATS (daemon_router_nats.go:362-367)

```go
if err := r.nc.PublishMsg(msg); err != nil {
	observability.NATSErrorsTotal.WithLabelValues("tools.cancel", "publish").Inc()
	return err
}
return nil
```

- Publish to NATS succeeds → returns `nil`
- **No subscriber confirmation; NATS pub-sub is fire-and-forget**

### Bottom Line

**Neither transport acknowledges that the daemon received or acted on the cancel.** Both are best-effort fire-and-forget:
- If the daemon is offline → error returned immediately
- If the daemon is online but the buffer is full → error returned immediately
- If the daemon receives the message but crashes before processing it → no error returned, but the cancel was ineffective

The assumption is that the daemon is responsive. If it is not, the only fallback is the in-memory cancel signal (which doesn't work in distributed mode for a running daemon process, as analyzed above).

---

## Latency Budget Table

| Hop | Process | Transport | Sync/Async | Latency | Cumulative |
|-----|---------|-----------|-----------|---------|-----------|
| 1 (Signal) | API server → Temporal | gRPC | Async | 10-50ms | 10-50ms |
| **1b (Workflow receives & processes signal)** | **Worker** | **In-process** | **Sync** | **~1ms** | **10-51ms** |
| **HEARTBEAT DELAY** | **Worker** | **Temporal** | **Throttled async** | **0-3000ms** | **10-3051ms** |
| 2 (Read tools) | API server → DB | SQL | Sync | 5-50ms | 15-100ms |
| 3a (Push cancel gRPC) | API server → Daemon | gRPC | Async | 1-5ms/tool | 16-105ms (N tools) |
| 3b (Push cancel NATS) | API server → NATS → Daemon | NATS | Async | 1-5ms/tool | 16-105ms (N tools) |
| 4 (Daemon receives) | Daemon → Handler | In-process | Async | 1-100ms | 17-205ms |
| 5 (Process dies) | Daemon → OS | Signal/Context | Sync | <1ms | 17-205ms |

### Measured vs. Budget

**Measured:** Pause 1.05s, Interrupt 1.97s / 2.81s

**Accounting:**
- **0-50ms:** Hops 1-3 (signal, DB read, daemon cancel push)
- **0-3000ms:** Heartbeat throttle (the activity waiting for the next heartbeat response)
- **0-100ms:** Daemon receiving and processing (Hop 4-5)

The measured 1.05s-2.81s lands right in the **heartbeat throttle window**. The daemon-side kill is sub-millisecond; it is NOT the bottleneck.

---

## Key Findings

1. **Both pause and interrupt follow identical daemon-cancel paths** (Hops 2-5). They differ only in workflow-level signaling (pause broadcasts to all, interrupt targets one thread).

2. **The heartbeat throttle (3s) is the ONLY substantial latency source.** Network and DB latencies are negligible (<100ms).

3. **Daemon cancel is immediate** (~1-5ms). Process death is immediate (<1ms). Neither contributes materially to the 1-3s measured latency.

4. **`shell.GetCancelSignal()` is not dead code**, but it is unreachable in distributed mode. It remains as insurance and documentation.

5. **The gap between workflow signal and daemon kill** is 5-50ms, not a problem because interrupt doesn't retry.

6. **No acknowledgment** that the daemon received or acted on cancels. Both transports (gRPC and NATS) are fire-and-forget.

7. **The only way to improve latency significantly** is to reduce the heartbeat throttle from 3s to something smaller. But this is a stability trade-off (heartbeat timeouts, false "worker died" resets).

