# 2026-09-29 — Reconciler killed a healthy chat as "wedged"; its live sub-agents were left reading failed

Chat / root workflow `abe58f03-fc55-42f3-a23a-7db19f934c9f` ("Barksocial Failed CI Pipelines (branch)"),
control-plane dev stack, `reliant` DB on `localhost:5434`. All times PDT unless marked Z.

## Summary

Nothing in the workflow was broken. The laptop was overloaded (load average ~70–80), Postgres on
:5434 stopped answering connects in time, and workflow tasks for this chat started exceeding
Temporal's 10s workflow-task timeout. Every timeout drops the sticky cache, so the next attempt
replays the full ~20 MB history on the same overloaded host and times out again. The attempt
counter climbed to 9, and the reconciler's wedge detector (`attempt >= 5`) terminated the run as a
"non-deterministic replay after a code update", which it was not.

Terminating the root killed its six in-flight sub-agents (they run inline in the root's Temporal
execution). The chat was then resumed with reset-and-replay, which **rebuilt those same six
sub-agents** — but nothing moved their workflow rows or thread rows back to running, so the UI
showed live agents as failed, and a reconciler sweep wrote six false "sub-agent finished but the
parent had exited" reports.

## Evidence (verified — do not re-derive)

Temporal history of the terminated run `0d87e6c2-481a-4836-a789-cb12122cc9f8`
(`temporal workflow show --address 127.0.0.1:7233 --namespace reliant -w <id> -r <run> -o json`):

| Event | Time (Z) | Type |
|---|---|---|
| 28070 | 04:42:54.99 | WORKFLOW_TASK_COMPLETED (later used as the reset point) |
| 28077 | 04:46:16 | WORKFLOW_TASK_TIMED_OUT (START_TO_CLOSE) |
| 28084 | 04:46:33 | WORKFLOW_TASK_TIMED_OUT (START_TO_CLOSE) |
| 28089 | 04:46:58 | WORKFLOW_TASK_TIMED_OUT (START_TO_CLOSE) |
| 28096 | 04:47:22 | WORKFLOW_TASK_TIMED_OUT (START_TO_CLOSE) |
| 28097 | 04:51:31 | WORKFLOW_EXECUTION_TERMINATED — "Workflow wedged: workflow task failing repeatedly (attempt 9) - likely non-deterministic replay after a code update; reset would re-diverge" |

- **Zero `WORKFLOW_TASK_FAILED` events** in the whole run. Only timeouts. Activities kept completing
  between them (28074, 28079, 28082, 28086). The workflow was progressing slowly, not stuck.
- History: ~28.1k events / ~19.7 MB — **below** this code's own continue-as-new threshold
  (`continueAsNewEventThreshold = 40000`, `continueAsNewSizeThreshold = 40 MiB`,
  `internal/workflow/runtime/continue_as_new.go:42`). History size made each replay slow; it did
  not hit a cap.
- Worker log: `TMPRL1104 … WorkflowTaskDuration=3m43s` (first slow task), then 2m20s, 1m2s, 51s…
- `reliant-api-server.log` 21:51:31 `[Reconciler] Workflow task is failing repeatedly (wedged) -
  terminating and marking as failed attempt=9`.
- Postgres :5434 connect timeouts start 21:43 (`dial error: timeout`, `failed SASL auth: timeout`).
  Not connection exhaustion (~130 of 300 used) — CPU starvation.

Detector: `internal/workflow/reconciliation/reconciler.go` — threshold at `:189`
(`DefaultWedgeAttemptThreshold = 5`), observation at `:643`, terminate at `:825-877`. It reads only
`PendingWorkflowTask.Attempt`, which counts timeouts and failures alike.

### Collateral: sub-agents

21:52:01 the reconciler's backstop sweeps closed the subtree of the terminated root:
`Reaped orphaned workflow descendants rows=6`, `Reaped orphaned threads rows=7`.

22:04:52 the user sent a message → `SendMessage` → `runs.Service.ResumeInterrupted` →
`PauseService.ResumeInterruptedWorkflow` → `resetInterruptedForResume` → `ResetInterruptedWorkflow`
reset to event 28070 → new run `bb7bf2e6-1578-4006-b5ef-99b745e92d43`.
`ResumeInterruptedWorkflow` then marked **only the root** workflow row Active.

- **No new thread or workflow rows were created after the reset.** The sub-agents executing in the
  new run are the same threads, rebuilt by replay (worker log for run `bb7bf2e6` shows
  `thread=d8502be3`, `30ba8cd8`, `02efe196`, `6bd172cd`, `e66d7fbe` doing work).
- Their rows still read failed:

| workflow row | thread | created_at (Z) | state / stop_reason | completed_at (Z) |
|---|---|---|---|---|
| 1f807b5b | 02efe196 | 03:30:03 | 3 / 2 | 04:52:01.138 |
| d46c32ba | 30ba8cd8 | 03:59:28 | 3 / 2 | 04:52:01.138 |
| e66d7fbe | e66d7fbe | 04:14:05 | 3 / 2 | 04:52:01.138 |
| c470299e | 2a9ce9f8 | 04:14:05 | 3 / 2 | 04:52:01.138 |
| 23aece8a | d8502be3 | 04:17:28 | 3 / 2 | 04:52:01.138 |
| f403c9f6 | 6bd172cd | 04:29:17 | 3 / 2 | 04:52:01.138 |

  Their threads are `status=4` (failed), `completed_at 04:52:01.28Z`. Siblings that genuinely
  finished before the reset point (e.g. `3384cd4b`, completed 04:30:49Z) are correctly completed.

- Why nothing revived them: the only write that moves a thread/workflow back to running is
  `WorkflowStatusActivity`'s `"started"` arm (`reviveThreadForNewRun`,
  `internal/workflow/runtime/activities/handlers/workflow_status.go:190,244`). After a reset,
  a child that was already running at the reset point never re-executes its "started" activity —
  it is in the replayed history. Only activities scheduled after the reset point re-run.

22:05:01 `repairStrandedBackgroundSpawns` saw children failed and the root Active (so its
`rootAwaitingResume` guard did not apply) and wrote six `agent_messages` rows
(kind 4, status 3 undelivered): "Sub-agent … finished, but the thread that spawned it had already
exited". False on both counts. Those rows occupy
`idx_agent_messages_one_terminal_report_per_spawn` (unique on `tool_call_id` where kind in 2,3,4),
so when those sub-agents really finish, the live `EnqueueAgentMessage` (plain INSERT,
`enqueue_agent_message.go:85`) will collide.

22:20:21 the user paused the chat.

## Decisions

1. **Reconciler interventions off, code kept.** The destructive detectors date from when the
   workflow system was unstable; today they fire on a sluggish laptop, not on bugs. Gate the three
   intervention paths — wedge terminate, stuck-task reset/terminate, progress-stall terminate —
   behind one config switch that defaults off. Detection may still log (WARN), but takes no action.
   Bookkeeping sweeps (drift repair, lost-workflow repair, orphan reaps, stranded-spawn repairs,
   orphaned mailbox resolution, silent-termination notice) stay on.
2. **A resume must revive what it resumes.** After any reset used to resume a run, every descendant
   workflow row that was live at the reset point — `created_at <= T_reset < completed_at`, stopped
   for any reason other than paused — goes back to running, along with the threads those rows own
   and the root's thread. Rows created after `T_reset` re-run their own "started" activity and
   self-revive; rows completed before `T_reset` stay completed because their completion is in the
   replayed history. The revival must land before the root is marked Active, so no reconciler pass
   can observe "root live, children failed".

## Before re-enabling interventions

The wedge detector must distinguish `WORKFLOW_TASK_TIMED_OUT` from `WORKFLOW_TASK_FAILED` (history,
or the pending task's last failure) and act only on failures. A timeout means slow, not stuck.

## Open follow-ups (not done here)

- Repair this chat's rows in the live DB (six workflow rows + six threads → running; delete the six
  false undelivered reports). This needs owner approval, because that DB holds real data.
- Make the live completion enqueue supersede a reconciler-written undelivered report, instead of
  colliding with it on the unique index.
- Consider a larger `WorkflowTaskTimeout` so a ~20 MB replay on a loaded host does not time out.
