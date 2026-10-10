# Queue a run until its machine is up (instead of failing preflight)

**Status:** shipped (#641, #672). The 10-minute budget and the "send your
message again" ending were replaced on 2026-10-10 by a durable wait and
server-side delivery — see §5, which supersedes the budget and the polling
loop described in §2.

**User report (verbatim):** "i sent a chat with no daemon connected (it was
waking up). we recently added some chats that can run with no daemons, which we
want to keep this functionality. however might be nice to queue chats instead of
this when daemon is required". Screenshot: two red cards, "Workflow error in
daemon_unavailable" and "Workflow error in Preflight Daemon Check · Attempt 1".

## 1. Settled facts (verified on main 3b95fb8a; do not re-derive)

- `PreflightDaemonCheckActivity.Execute`
  (`internal/workflow/runtime/activities/handlers/preflight_daemon.go`):
  `chat.NoMachine` → `{DaemonAvailable:false}, nil` (no-machine chats never wake —
  KEEP). Otherwise `router.IsDaemonOnline(ctx, userID)` (per-user, not
  selector-aware); infra error → available; online → available; offline →
  `waker.EnsureAwake(ctx, userID, selector)`: nil → available (even though a
  resumed daemon is NOT attached yet); ANY error (including
  `toolexec.IsDaemonPending`, i.e. "still starting") → hard error
  "this workflow requires a daemon but none is available…".
- Call site: `internal/workflow/runtime/workflow.go` STEP 6.07, gated on
  `RequiresDaemon(wf, buildPreflightConfig())`. `MaximumAttempts: 1`,
  `StartToCloseTimeout: 30s`. On error: `notifyWorkflowError(..., "daemon_unavailable", ...)`
  then `return nil, fmt.Errorf("preflight daemon check failed: %w", err)`.
- `ActivityWrapper` (`internal/workflow/runtime/registry.go`) writes an error
  card to chat_updates for EVERY failed activity attempt (`writeErrorEvent`).
  That is the "Preflight Daemon Check · Attempt 1" card. So a wait implemented
  as "return an error and let Temporal retry" would spam cards. Waiting must be
  done with SUCCESSFUL activity results.
- The wrapper heartbeats every 500ms; cancellation reaches an activity within
  ~2s via heartbeat regardless of HeartbeatTimeout. On worker stop the wrapper
  cancels the activity ctx early so the still-alive worker can report.
- `resolveMaxAttempts` keys on `HeartbeatTimeout == activityHeartbeatTimeout`
  (30s) → assumes a graph step (5 attempts). Give preflight a DIFFERENT
  heartbeat timeout and add `"PreflightDaemonCheck"` to the named switch, or the
  terminal failure renders as "Retrying (attempt 1/5)".
- The UI's Stop is `TerminateChat` → Temporal `TerminateWorkflow`; a Temporal
  `CancelWorkflow` (chat_crud.go's other paths, `temporal workflow cancel`)
  reaches timers and activities. Neither may post a `daemon_unavailable` error
  card during the wait. (Both verified on a live stack, §4.)
- `trackDaemonPending` in `execute_tools.go` already drives
  `repo.SetChatDaemonBlocked(ctx, chatID, bool)` (transition-only write, emits a
  chat update). The `chats_with_activity` view reports
  `ChatActivity.WAITING_FOR_DAEMON` (5) only when `daemon_blocked_at IS NOT NULL`
  AND a workflows row for the chat has `state = 2` (running).
- The workflows row is created/flipped to running by `notifyWorkflowStatus(...
  "started" ...)` at STEP 6.5 — AFTER preflight (6.07). So during today's
  preflight there is no running row and WAITING_FOR_DAEMON can never show.
- Web already renders WAITING_FOR_DAEMON: `ComposerWakeStatus` ("Waking
  <machine>…" / "Waiting for your machine…" + "Continue without machine"),
  `RunMachineBanner`, `runStatus` → "Waiting for machine". The transcript's
  `ChatThinkingIndicator` does NOT — it cycles "Thinking/Processing…".
- `findResumeResetPoint` (`internal/workflow/reset.go`) resumes a FAILED run by
  resetting to the decision that scheduled the activity named in the close
  error chain. If the final failure is not an activity error, resume falls back
  to a heuristic that REPLAYS the failure (the 2026-09-08 incident). So the
  terminal "machine never came up" failure must stay an ACTIVITY error.
- Replay policy (`internal/workflow/runtime/replaytest/fixtures/README.md`):
  never add `workflow.GetVersion` gates; change the command sequence and
  regenerate fixtures with `make replay-fixtures` (needs Postgres + an
  ephemeral Temporal dev server). **Superseded 2026-10-10:** this change
  shipped ungated and wedged every in-flight run recorded with the old order
  (chats 97654413, 3f03dc31). The order is now gated by
  `preflightAfterStartedChangeID`, the old shape is pinned by the frozen set
  `replaytest/fixtures/frozen/2026-10-08-preflight-before-started`, and the
  README now requires a gate plus a frozen set for any command-sequence change.
- `NATSDaemonRouter` always implements `toolexec.DaemonWaker`. Record states:
  attached (routable), unconfirmed (self-hosted with no fresh lease, or managed
  ready/failed), starting (provisioning/cloning → `ErrDaemonPending`), suspended
  (wakeable). `Wake` returns `WakeResult{Resumed}`; `EnsureAwake` drops it.

## 2. Design

Preflight distinguishes three outcomes instead of two:

| Outcome | When | Result |
|---|---|---|
| **ready** | `IsDaemonOnline` true, or an infra error checking (existing leniency), or non-remote executor, or `chat.NoMachine` (unchanged: `{DaemonAvailable:false}`) | `{daemon_available, daemon_id}`, nil |
| **waiting** | offline, and the wake either succeeded (resume under way / unconfirmed record) or returned `ErrDaemonPending` (starting) | `{waiting: true}`, nil — and `SetChatDaemonBlocked(chat, true)` |
| **unavailable** | offline and the wake failed for any other reason (no daemon at all, selector matches nothing, resume refused, no credential, `ErrAutomationAccessNotGranted`) | error (as today; include the cause, it is user-facing text) |

**The activity waits in bounded slices.** New input fields:
`wait_seconds` (int; 0 = single check, today's test shape) and `final` (bool).
The activity checks; if waiting, it wakes at most ONCE per execution, then polls
`IsDaemonOnline` every ~2s until online (→ ready, clear the blocked marker),
the slice elapses (→ `{waiting:true}`), or its ctx is done (→ `{waiting:true}`,
nil — on worker stop this lets the dying worker report success; on a workflow
cancel the workflow's `Get` returns CanceledError anyway). If the slice elapses
and `final` is set, it clears the marker and returns the terminal error:
"Your machine didn't come online within N minutes. …". All marker writes on
exit paths use `context.WithoutCancel`.

**The workflow loops** (only on the waiting path, so the ready path's command
sequence is one activity, as today):

```
waitStart := workflow.Now(ctx)
for {
    final := workflow.Now(ctx).Sub(waitStart)+preflightWaitSlice >= preflightWaitBudget
    run PreflightDaemonCheck {chat_id, daemon_selector, wait_seconds, final}
    err → if canceled: return err (no error card); else notifyWorkflowError(daemon_unavailable) + return wrapped (as today)
    !waiting → break
    workflow.Sleep(ctx, short backoff)   // guard against a hot loop if a slice returns early
}
```

Constants (named, with a why-comment): budget 10 minutes (covers a managed
resume and a fresh provision, bounds a stuck one), slice 60s, poll 2s, backoff
2s. Activity options: `StartToCloseTimeout = slice + margin`, a dedicated
`HeartbeatTimeout` (not 30s, see §1), `MaximumAttempts: 1`.

**Move STEP 6.07 after STEP 6.5** ("started"), before the greenfield probe
(6.8, whose comment already says it runs after preflight). That creates the
running workflows row first, so `daemon_blocked_at` surfaces as
WAITING_FOR_DAEMON and the existing composer/banner UI lights up. This changes
the command order → regenerate replay fixtures.

**Web:** `ChatThinkingIndicator` shows "Waiting for your machine" (no cycling)
when the chat's activity is WAITING_FOR_DAEMON. `ChatPresenter` already computes
`currentChat?.activity === ChatActivity.WAITING_FOR_DAEMON`.

### Alternatives rejected

- *Temporal activity retries on a "pending" error*: one red card per attempt
  (ActivityWrapper), and a misleading "Retrying" chip.
- *One long activity (10 min)*: a worker restart mid-wait fails the run; slices
  make a restart cost nothing.
- *Pure workflow timers with an instant activity per poll*: ~8 history events
  per 2s poll; slices with in-activity polling give 2s latency for ~10
  activities over the whole budget.
- *Queue at the API (hold the message, start the run on daemon attach)*: needs a
  new attach-event → launch path and a second queue beside Temporal's. The run
  is already durable; holding it at its first step is the queue.
- *Notify "started" early only on the waiting path*: avoids fixture churn but
  forks the startup sequence; policy is to cut cleanly.

### Unchanged (must stay)

- No-machine chats: no wake, no wait, `{DaemonAvailable:false}`.
- `RequiresDaemon` and its gating.
- A daemon that exists and is online → one activity, no waiting.
- Unattended (trigger) runs: same delegated-token wake rules (`automationcred.Allow`).

## 3. Known follow-ups (not in this change)

- ~~`IsDaemonOnline` is per-user.~~ Fixed: `DaemonRouter.IsDaemonOnline` takes a
  `*DaemonSelector` (nil = any of the user's daemons). With a selector the
  router asks the gateway-local resolver, else checks each matching registry
  record with `daemonliveness.Reachable` (NATS status query, falling back to
  the new `Repository.IsDaemonIDAttached` lease check). Preflight passes its
  selector to both the first check and the poll loop.
- The terminal failure still shows two cards (wrapper card + `daemon_unavailable`).
  The `daemon_unavailable` card now shows the activity's own message rather than
  Temporal's `activity error (type: …, scheduledEventID: …)` wrapping.

## 4. Verified on a live distributed stack (2026-10-08)

Local compose stack (postgres, temporal, nats, api-server, temporal-worker,
daemon-gateway, tools-daemon), `builtin://agent`, no LLM key (runs end at
CallLLM, after preflight):

- Daemon up: one `PreflightDaemonCheck`, 38 ms, scheduled after "started".
- Daemon stopped, chat sent: activity 5 (WAITING_FOR_DAEMON), `daemon_blocked_at`
  set, workflow state 2, 60s slices with a 2s timer between, no
  `daemon_unavailable` card. Daemon restarted ~93s in (across a slice boundary):
  preflight completed 1.6s after the gateway registered it, marker cleared, run
  moved on to CallLLM.
- UI Stop (terminate) and `temporal workflow cancel` mid-wait: marker cleared,
  no error card.
- Budget exhausted: 10 slices, then the "didn't come online within 10 minutes"
  activity error, marker cleared, run failed.

The repo's own `docker-compose.yml` does not run as-is (Dockerfile targets that
do not exist, auth-gated base images, a NATS flag removed in 2.15, Temporal
namespace mismatch, per-service vault keys, self-signed TLS); a working
override was used.

## 5. A queued message is never dropped (2026-10-10)

Prod report after #672: "Sending a new message while the daemon is spinning up
is supposed to enqueue it, but it just looks like it gets stuck." Two gaps
remained: the wait gave up after 10 minutes (a cold start with an image pull,
or a crash-looping machine the controller later rolls, outlasts that) and the
run then failed with the message unsent; and a run the reconciler ended as
wedged left the user's "continue" unanswered until they sent it again.

**The wait** (`runtime.waitForDaemon` / `waitForMachine`). The first check is
unchanged: wake once, poll every 2s for up to 60s. After that no activity runs
while the run waits: it sleeps on a durable timer (30s, doubling to 5m) and
checks once (a 15s poll) when the timer fires or when its machine signals
(`machinewait.SignalName`). The api-server sends that signal to every run of a
user parked waiting (`chats.daemon_blocked_at` on a running root) when one of
their machines connects (`internal/queueddelivery`, a durable consumer on
`daemon.v1.events.connected`). The wait ends:

- machine attached — the run proceeds;
- machine FAILED (unattached, mirrored lifecycle `failed`) or REMOVED (no
  record matches) — `toolexec.NATSDaemonRouter.MachineState`, read by the
  activity after the wake and at each slice's end;
- "Continue without machine" — `BranchChat` with `no_machine` signals
  `{abandon: true}`; the run ends CANCELLED (a CanceledError, which
  `handleWorkflowCompletion` now records as cancelled);
- the 6-hour cap (`preflightMachineWaitCap`), the safety net for a forgotten
  chat.

No version gate: the new loop issues the same command sequence as the old
(timer, activity, timer, …; a replayed timer matches by id, not duration), so
runs in flight at the deploy replay into the new wait. Pinned by the frozen
fixture set `2026-10-10-machine-wait-ten-minute-budget` (recorded with the old
code), plus `machine_wait.json` / `machine_wait_signaled.json`.

**The message stays queued.** Every terminal end of an attended run's wait
(failed, removed, none available, cap) sets `chats.queued_for_machine_at` and
says "Your message is queued and will be sent as soon as your machine
connects". The chat's activity reads `CHAT_ACTIVITY_QUEUED_FOR_MACHINE` (6)
while no run is live: the transcript shows "Queued — will send when your
machine is back", the composer keeps Try again / Continue without machine.
Cleared by whatever starts the next run: a send, the delivery below, or the
preflight that finds the machine up; dropped by Continue without machine.
Unattended (trigger) runs are not queued — their failure is the trigger's to
report.

**Delivery.** `ChatService.ContinueQueued` starts the run the user's next send
would have, without the new message: SendMessage's resume of an interrupted
run (reset-and-replay when the history replays, else a fresh run at the
checkpoint with the ended run's own inputs). It runs under the chat's
run-control lock, claims the marker atomically, and starts nothing unless the
root run is FAILED in the database and closed in Temporal, so concurrent
callers deliver once. Callers: a machine connect (`queueddelivery`), the
reconciler's sweep (backstop for a missed connect), and the reconciler right
after it ends a wedged run whose thread holds an unanswered user message or a
queued mailbox row. The orphaned-mailbox sweep leaves a queued chat's rows
alone.
