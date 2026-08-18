# Fast cancellation — shared briefing for sub-agents

Working notes for the interrupt/pause fast-cancel work. Canonical spec is
`specs/interrupt-pause-spec.md`; this file is the shared context for the
parallel investigation. **Do not delete this file** — the parent agent owns
cleanup.

## The goal

Cancellation (pause = whole chat, interrupt = one thread) must reach running
work in ~milliseconds, everywhere, through ONE generic mechanism. Today it takes
1-3s. Pause and interrupt must differ ONLY in scope.

## Architecture facts — VERIFIED, do not re-derive

- **Distributed mode is the ONLY supported mode.** Monolith does not exist.
  Anything that only works in-process is dead code, not a "fallback".
- Three processes: **API server** (`internal/serverapi`), **Temporal worker**
  (`internal/serverworker`), **daemon-gateway** (`internal/servergateway`,
  holds daemon gRPC connections). The user's daemon runs on their machine.
- Activities (`call_llm`, `execute_tools`, …) run **in the worker**.
- Tools dispatch to the daemon; the daemon runs each execution under
  `context.WithCancel(context.Background())` and keeps a registry of
  `CancelFunc` keyed by request_id AND tool_call_id
  (`internal/toolexec/daemonruntime/runtime.go:1007`, cancel at `:1237`).
  A cancel push → `exec.CommandContext` → process group. This path is FAST and
  does not involve Temporal.
- Both API server (`serverapi/run.go:310`) and worker (`serverworker/run.go:234`)
  construct their own `NATSDaemonRouter`. The worker's is used by
  `remote_executor.go:322` to push a daemon cancel when a call is abandoned.

## What an activity IS (this tripped the parent agent up — don't repeat it)

An active thread is a **running goroutine in the worker**. Nothing is parked;
there is no "waking". `call_llm` sits in a stream-event loop, `execute_tools`
sits waiting on a daemon response. The ONLY stop signal is **context
cancellation** — `ctx.Done()` closes and the already-running goroutine notices
at its next check (`call_llm.go:1010`, `execute_tools.go` `ctx.Err()`).

Park-and-wake IS real, but only on the WORKFLOW side (`checkPause` →
`workflow.Await` on an epoch; `broadcastResume` bumps it). Do not carry that
model into the activity world.

`call_llm` and `execute_tools` are NOT structurally different. Both are worker
goroutines stopped by ctx cancellation. The only difference is that
`execute_tools` farmed work out to a second process, so that process must also
be told — which is cleanup, not a different cancel mechanism.

## The heartbeat situation — CORRECTED, this is the crux

- `ActivityWrapper` (`internal/workflow/runtime/registry.go:499-531`) spawns a
  background goroutine per activity calling `activity.RecordHeartbeat` every
  **500ms** (`activityHeartbeatInterval`, `registry.go:250`). It wraps EVERY
  activity. So decoupled background heartbeating is ALREADY IMPLEMENTED.
  (An earlier claim that "execute_tools never heartbeats" was WRONG — the
  heartbeat is in the wrapper, not the handler files.)
- `call_llm` ALSO heartbeats from its own stream loop (`call_llm.go:1022`).
- BUT: `MaxHeartbeatThrottleInterval: 3 * time.Second`
  (`internal/workersetup/setup.go:134`) makes the SDK coalesce those 500ms
  ticks and only reach the server every 3s.
- `activityHeartbeatTimeout = 30s` (`registry.go:269`). The comment at
  `registry.go:262-269` says the effective floor is the THROTTLE not the
  interval, and that timeout and throttle must move together (it was 10s when
  the throttle was 500ms).
- `spuriousHeartbeatCancel` (`registry.go:296`) exists because the SDK cancels
  the activity ctx on ANY retryable heartbeat RPC error — one slow round trip
  killed healthy activities. Read its doc comment before proposing changes.
- Measured live: pause 1.05s, interrupt 1.97s / 2.81s.
- `origin/main` has the SAME 3s throttle and 30s timeout (verified via
  `git show`). This is NOT a regression from main.

## THE OPEN QUESTION

Temporal delivers cancellation to a running activity ONLY in a heartbeat
RESPONSE. We heartbeat every 500ms but throttle delivery to 3s.

Some Temporal docs claim the SDK will "force a poll ahead of schedule if there
is a pending cancellation request". If that were true in the Go SDK, cancels
would already arrive in ~500ms and we would not measure 1-3s. So either that
fallback does not exist in Go, or the throttle defeats it. **Determine which,
from SDK SOURCE, not from docs.**

Also open: is there any Go SDK call an activity can make to actively PULL
pending cancellation state, rather than waiting for a heartbeat response?

## SDK ANSWER — read from source, v1.37.0. THE DOC CLAIM IS FALSE.

Read `internal/internal_task_handlers.go` in
`/Users/seanteeling/go/pkg/mod/go.temporal.io/sdk@v1.37.0`.

**There is NO early-poll-on-pending-cancellation.** `temporalInvoker.Heartbeat`
(`:2080`) is the whole decision, and it is unconditional:

```go
if i.hbBatchEndTimer != nil && !skipBatching {
    i.lastDetailsToReport = &details
    return nil          // <-- SWALLOWED. No RPC. No cancel check.
}
```

If a batching window is open, the heartbeat does NOT hit the server — it just
overwrites `lastDetailsToReport` and returns nil. Nothing consults cancellation
state, because the worker CANNOT know a cancel is pending: **the cancel is only
learned FROM the heartbeat response** (`internalHeartBeat` `:2150` — the RPC
returns `*CanceledError`, which calls `i.cancelHandler(err)`). It is strictly
pull-only, and the pull is what is being throttled.

So calling `RecordHeartbeat` more aggressively from the activity CANNOT help.
Our 500ms ticks are swallowed by the 3s window. The batch timer
(`:2099`, `time.NewTimer(i.heartbeatThrottleInterval)`) fires, sends the last
details, and only THEN can a cancel arrive. **Cancel latency == throttle
interval.** That is the entire 1-3s.

`skipBatching` is the only bypass; it is set by `RecordHeartbeat` internals for
local activities, not something we control from an ordinary activity.

**The throttle is ALSO the RPC deadline — our repo comment is CORRECT**
(`internalHeartBeat` `:2138-2145`):
```go
recordTimeout := i.heartbeatThrottleInterval
if recordTimeout < minRPCTimeout { recordTimeout = minRPCTimeout }
```
`minRPCTimeout = 1s` (`internal/internal_utils.go:28`). So lowering the throttle
BELOW 1s does NOT shorten the RPC deadline — it floors at 1s. This kills the old
fear: at throttle=500ms the deadline was already 1s, not 500ms.

**Where the throttle comes from** (`getHeartbeatThrottleInterval` `:2361`):
`0.8 * HeartbeatTimeout`, capped by `MaxHeartbeatThrottleInterval`.
Our HeartbeatTimeout is 30s → 0.8*30s = 24s → capped to our 3s. So the 3s cap is
what binds. **Lowering `MaxHeartbeatThrottleInterval` directly lowers cancel
latency**, and does NOT require changing `activityHeartbeatTimeout` (30s), since
30s is about detecting a DEAD worker and is independent of the cap.

Also confirmed: `internalHeartBeat` `:2169` calls `cancelHandler` on ANY
retryable error — this is exactly the hazard `spuriousHeartbeatCancel` was
written for. Lowering the throttle raises heartbeat RPC frequency, so evaluate
that interaction.

## `shell.GetCancelSignal()` IS DEAD CODE — parent verified, overriding a sub-agent

A sub-agent reported this as "NOT dead code ... load-bearing insurance for daemon
failure ... keeping it is harmless." That is WRONG and self-contradicting (it
also states the write and reads are in separate processes with separate
singletons — which is the definition of dead).

Verified:
- It is a plain package-level singleton over an in-memory map, guarded by
  `sync.Once` (`internal/llm/tools/shell/cancel_signal.go:17-30`). No IPC, no
  persistence, no transport.
- **Every writer is in the API server**: `internal/grpc/services/tool_call.go:83`
  and `internal/threads/interrupt.go:259` (reached only via
  `chat_interrupt.go:33` and `chat_crud.go:1051`, both `grpc/services`).
- **Every reader is in the worker**: `execute_tools.go:553` and `:683`.
- The worker's own `threads.Service` is built with NO toolCanceler
  (`internal/workersetup/setup.go:71`), and `internal/serverworker` does not
  import `internal/grpc/services` at all.

Two separate processes ⇒ two separate singletons ⇒ the flag NEVER crosses.
`IsCancelled` in the worker can only ever return false. It is not a fallback
and cannot provide "insurance" against anything: an in-memory map in process A
is unreachable from process B, always.

Distributed is the ONLY supported mode, so there is no configuration in which
this works. **Delete it** (and the `SetCancelled` interface method on
`threads.ToolCanceler`) rather than reasoning about it. Any comment claiming it
is "authoritative even when the daemon is unreachable" (`interrupt.go:255-258`)
is false and goes with it.

Consequence for design: the cancel-before-dispatch check at
`execute_tools.go:553` — which is how a cancelled-while-PENDING call avoids
running — is currently INERT in production. Whatever replaces it must be a real
cross-process mechanism.

## SDK v1.47.0 VERIFIED — every mechanism is UNCHANGED from v1.37.0

Checked before upgrading, by diffing the actual source of both versions.
All four load-bearing mechanisms are byte-for-byte identical:

1. `temporalInvoker.Heartbeat` — the unconditional batching swallow
   (`if i.hbBatchEndTimer != nil && !skipBatching { ...; return nil }`) is
   IDENTICAL. Still no early poll, still no cancellation check.
2. `internalHeartBeat` — `recordTimeout := i.heartbeatThrottleInterval;
   if recordTimeout < minRPCTimeout { recordTimeout = minRPCTimeout }` IDENTICAL.
3. `minRPCTimeout = 1 * time.Second` (`internal/internal_utils.go:29`) IDENTICAL.
4. `getHeartbeatThrottleInterval` — `0.8 * HeartbeatTimeout` capped by
   `MaxHeartbeatThrottleInterval`; `defaultMaxHeartbeatThrottleInterval = 60s`.
   IDENTICAL.

**Conclusion: upgrading the SDK does NOT change cancellation latency.** Cancel
latency == throttle interval on both versions. Upgrade on its own merits
(10 minor versions of fixes), NOT as a fix for this. Do not let the upgrade
block the throttle change; they are independent.

Server images are already `temporalio/auto-setup:latest` /`temporalio/ui:latest`
(docker-compose.yml:19, docker-compose.cloud.yml:57), so the "infra" half is
already tracking latest in dev.

## CORRECTION to the design-survey sub-agent on spuriousHeartbeatCancel

The survey said an out-of-band cancel would have "no Temporal cause", would look
like a failed heartbeat, and would be RETRIED — fixed by whitelisting a new
cause. **That reads the function backwards.** `registry.go:304-308`:

```go
cause := context.Cause(ctx)
if cause == nil || errors.Is(cause, context.Canceled) {
    return false          // NOT spurious -> treated as a REAL cancellation
}
```

`spuriousHeartbeatCancel` returns **true** (⇒ retry) only for a cause that is
non-nil AND unrecognized. So:
- Cancelling with a plain `context.CancelFunc` (cause = `context.Canceled`) ⇒
  returns false ⇒ correctly treated as a real cancel. SAFE.
- Cancelling with a NEW custom cause ⇒ non-nil, unrecognized ⇒ returns TRUE ⇒
  the activity gets RETRIED. **That is the hazard, and it is created by
  inventing a cause, not by omitting one.**

So if we add a custom cause (desirable — it distinguishes our cancel in logs and
lets each activity know why it is stopping), we MUST also whitelist it at
`registry.go:312-314`. Same one-line change the survey proposed, opposite reason.
Get this right; a wrong reading here silently re-runs non-idempotent tools.

## Rules

- ⛔ NO `git stash` / `checkout` / `reset` / `commit`. Shared worktree with
  other agents' uncommitted work.
- ⛔ NO `pkill`, no `forge env down`. You are running inside forge.
- DB tests SILENTLY SKIP and print `ok` without `DATABASE_URL`. Always grep
  verbose output for `--- SKIP`. Use
  `DATABASE_URL='postgres://postgres:postgres@localhost:5433/reliant_reliant_reliant?sslmode=disable'`.
  NEVER port 5434 — that is control-plane's, with real data.
- Report actual command output. A confident false "it works" is much worse than
  a clear "this blocked here".
