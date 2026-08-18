# Temporal Go SDK v1.37.0 — Fast Cancellation Analysis

## Q1: Counter-Evidence for No Early Poll on Pending Cancellation

**VERDICT: CONFIRMED. No such path exists.**

The only mechanism to deliver cancellation to an activity context is through the heartbeat RPC response. Searched comprehensively for alternative paths:

- **Workflow-side cancel events** (`internal_event_handlers.go`): these are processed by the workflow executor, not delivered to activities out-of-band
- **Service errors (NotFound, NamespaceNotActive)**: delivered only via heartbeat RPC (line 2155-2159)
- **Activity pause/reset** (`ErrActivityPaused`, `ErrActivityReset`): delivered only via heartbeat response fields (line 2420, 2422, 2163-2166)
- **Worker shutdown** (`workerStopCh`): monitored in batching timer goroutine (line 2105) but only affects the batching window, not the activity context itself
- **No server-push or out-of-band delivery**: all cancellation state is pulled via `RecordActivityTaskHeartbeat` RPC

The batching window itself (line 2084-2087) returns immediately without checking cancellation:
```go
if i.hbBatchEndTimer != nil && !skipBatching {
    i.lastDetailsToReport = &details
    return nil  // ← NO RPC, NO CANCEL CHECK
}
```

**File:Line evidence:**
- Batching swallow: `internal_task_handlers.go:2084-2087`
- Only cancel invocation: `internal_task_handlers.go:2153`, `2158`, `2165`, `2172` — all from `internalHeartBeat`
- Context creation with cancel: `internal_task_handlers.go:2243` with single cancel passed to `newServiceInvoker`
- Activity context check: `internal_task_handlers.go:2311` checks `context.Cause(ctx)` is `IsCanceledError`

## Q2: Real Floor on MaxHeartbeatThrottleInterval

**VERDICT: 1 second (minRPCTimeout). Lowering below 1s does NOT tighten the RPC deadline.**

### Mechanism (internal_task_handlers.go:2138-2145)
```go
recordTimeout := i.heartbeatThrottleInterval  // e.g., 500ms
if recordTimeout < minRPCTimeout {             // minRPCTimeout = 1s
    recordTimeout = minRPCTimeout              // cap to 1s
}
ctx, cancel := context.WithTimeout(ctx, recordTimeout)
```

**File:Line:** `internal/internal_utils.go:28` defines `minRPCTimeout = 1 * time.Second`

### Throttle Derivation (internal_task_handlers.go:2361-2383)
```go
heartbeatThrottleInterval = 0.8 * HeartbeatTimeout  // capped by max
if heartbeatThrottleInterval > maxHeartbeatThrottleInterval {
    heartbeatThrottleInterval = maxHeartbeatThrottleInterval
}
```

For reliant: `0.8 * 30s = 24s`, capped to 3s (MaxHeartbeatThrottleInterval). Cap binds.

### All Uses of heartbeatThrottleInterval
1. **Timer fire interval** (`2099`): `time.NewTimer(i.heartbeatThrottleInterval)` — sets batching window duration
2. **RPC deadline base** (`2141`): `recordTimeout := i.heartbeatThrottleInterval` — used as deadline floor
3. **Activity execution context** (line 2303): separate `context.WithDeadline(ctx, info.deadline)` where deadline = `StartedTime + StartToCloseTimeout`, independent of heartbeat throttle

### RPC Load Impact
At throttle = T, N concurrent activities:
- **500ms throttle**: ~2N RPCs/sec (6x increase from 3s baseline)
- **200ms throttle**: ~5N RPCs/sec (15x increase)
- **RPC deadline remains 1s** (floors at minRPCTimeout)

### What Breaks Below 1s Throttle
Nothing technically breaks. The RPC deadline simply doesn't tighten:
- Throttle = 100ms → deadline = 1s (not 100ms)
- Throttle = 500ms → deadline = 1s (not 500ms)

But **worker load increases dramatically**: more goroutines, more context-switching, more pending heartbeats queued during the 1s RPC deadline window. This increases memory pressure and can destabilize the worker under high concurrency.

## Q3: API to Pull Pending Cancellation Without Heartbeat

**VERDICT: NO. There is no such API.**

`activity.GetInfo()` returns `ActivityInfo` struct (`activity.go:24-52`) with fields:
- `TaskToken`, `WorkflowType`, `WorkflowExecution`, `ActivityID`, `HeartbeatTimeout`, `Deadline`, `Attempt`, `RetryPolicy`, etc.

**No cancellation state field.** No `IsCanceled()` method, no `GetCancellationState()` function exists in the public or internal API. Cancellation is a context-only signal, delivered via `context.Done()` and `context.Cause()`.

**File:Line:** `internal/activity.go:24-52` — ActivityInfo struct definition shows all available fields

## Q4: skipBatching Usage and Call Sites

**VERDICT: Used only by session activities; ordinary activities cannot access it.**

### Call Sites
1. **Session activity heartbeat** (`session.go:436`): `skipBatching=true`
   ```go
   return activityEnv.serviceInvoker.Heartbeat(ctx, nil, true)
   ```
   Reason: Session framework controls heartbeat timing; internal batching would break session guarantees.

2. **Public RecordActivityHeartbeat** (`internal_activity.go:399`): `skipBatching=false`
   ```go
   _ = a.env.serviceInvoker.Heartbeat(ctx, data, false)
   ```

3. **Batching timer goroutine** (`internal_task_handlers.go:2126`): `skipBatching=false`
   ```go
   _ = i.Heartbeat(ctx, *detailsToReport, false)
   ```

**Ordinary activities cannot bypass batching.** The `skipBatching` parameter is not exposed in the public activity package. Only session activities (which are a specialized pattern) use it.

**File:Line evidence:**
- Session override: `internal/session.go:430-436`
- Public API: `internal/internal_activity.go:399`
- Batching call: `internal/internal_task_handlers.go:2126`
- Interface definition: `internal/activity.go:283` (ServiceInvoker.Heartbeat signature)

## Q5: context.Cause When Cancellation Arrives

**VERDICT: The error from recordActivityHeartbeat is set as the cause.**

When `internalHeartBeat` receives an error from `recordActivityHeartbeat`, it calls:
```go
i.cancelHandler(err)  // cancelHandler is context.CancelCauseFunc
```

This sets `context.Cause(ctx)` to that error. The activity then checks (line 2311):
```go
isActivityCanceled := ctx.Err() == context.Canceled && IsCanceledError(context.Cause(ctx))
```

### Possible Causes
- `*CanceledError` (from `heartbeatResponse.GetCancelRequested()`, line 2419)
- `*serviceerror.NotFound`, `*serviceerror.NamespaceNotActive`, `*serviceerror.NamespaceNotFound`
- `ErrActivityPaused`, `ErrActivityReset`
- Retryable errors (line 2171)

**File:Line evidence:**
- Context creation: `internal_task_handlers.go:2243` with `context.WithCancelCause`
- Cancel invocation: `internal_task_handlers.go:2153-2173` with various error types
- Activity check: `internal_task_handlers.go:2311` using `context.Cause(ctx)`
- Go context documentation: `context.CancelCauseFunc` sets the cause for retrieval by `Cause()`

---

## Summary

**No early-poll fallback exists.** Cancellation is strictly pull-based: the SDK fetches heartbeat responses every T seconds (where T = MaxHeartbeatThrottleInterval, floored by minRPCTimeout=1s). The response carries the cancel flag; if present, the context is immediately cancelled with the error as cause.

**Lowering MaxHeartbeatThrottleInterval below 1s does not tighten the RPC deadline.** The deadline always floors at 1s. A 500ms throttle produces the same 1s RPC deadline as a 3s throttle—but increases RPC volume 6x and worker load proportionally.

**No pull API exists.** Activities cannot check cancellation state without waiting for a heartbeat RPC response (which processes the server's cancel flag in the response).

**Ordinary activities cannot skip batching.** Only session activities can force immediate heartbeats; the `skipBatching` parameter is internal.

**Context cause is the error from the heartbeat RPC.** Usually `*CanceledError`, but can be service errors or pause/reset errors. The activity checks this via `context.Cause(ctx)` and `IsCanceledError()`.

The minimum achievable cancellation latency is **heartbeatThrottleInterval + RPC round-trip time**. At reliant's 3s throttle, cancellation arrives 3s + network latency after the server decides to cancel.
