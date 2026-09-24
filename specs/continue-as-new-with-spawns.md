# Continue-as-new with live background spawns + history-limit recovery

Status: designed, implementing (branch `workflow-history`). Pre-launch: no
backwards compatibility, no version gates.

## Incident recap (chat 0e15fdba)

- `continue_as_new.go` refuses to hand off while any detached spawn is live
  (`quiescentForContinueAsNew`). Past the 40 MB threshold the main loop hit the
  boundary 9 times (iterations 96–104) and declined every time: six sub-agents
  were running. They generated ~90% of the history bytes and pushed it from
  40 MB to 52 MB (7k events) in 20 minutes while the main thread did 2
  activities. Temporal terminated the run.
- The CAN check only runs at the TOP-LEVEL loop's iteration boundary. While the
  main thread is parked in `awaitLiveDetachedSpawns`, no check runs at all.
- Recovery made it worse: `ResumeInterruptedWorkflow`'s futility guard only
  looks at EVENT COUNT (`historyLen >= 51200-500`). The run died on SIZE at
  38k events, so it was reset (forking inside the 52 MB history) and died
  again 2.5 minutes later. `ResetAttemptGuard.Allow` treats any history growth
  as progress, so every user message would repeat that.

## Part 1 — recovery must never reset a history-limit death

Classify by the close event, not a proxy:

- In `ResumeInterruptedWorkflow` (internal/workflow/pause_service.go), for
  status TERMINATED read the close event (`GetWorkflowHistory(...,
  HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT)` — the reconciler already does this at
  reconciler.go ~2205) and treat `WorkflowExecutionTerminatedEventAttributes.reason`
  mentioning the history limit ("Workflow history size exceeds limit." /
  "Workflow history count exceeds limit.") as `ErrHistoryLimitExceeded`.
- Also keep a SIZE backstop: `DescribeWorkflowExecution` exposes
  `HistorySizeBytes`; treat `>= 50 MB - headroom` like the count check.
- Any reconciler auto-reset path (reconciler.go `resetGuard`) gets the same
  classification: never reset a history-limit death.
- `ErrHistoryLimitExceeded` already routes to the coarse fresh-restart at the
  checkpoint (new execution, empty history, thread history as truth). Verify
  that path end-to-end; with Part 2 it must also relaunch the spawns that were
  live (Part 2's handoff record is exactly what the fresh restart needs).

## Part 2 — continue-as-new that carries live spawns

A background spawn is a `workflow.Go` goroutine inside this execution
(`dispatchSpawnBackground`). Its durable state already lives outside Temporal:
the child thread's messages, the child workflow row, the `tool_calls` row
(status backgrounded), and its mailbox link to the parent. What exists ONLY in
memory is the goroutine and the `childTracker.liveDetachedSpawns` record
`{ToolCallID, ChatID, ParentThread, ChildThread}` plus the preset/target the
child runs with. So a spawn can be carried across CAN the same way the main
loop is: stop it at an iteration boundary, record where it is, restart it in
the successor from thread history.

### Handoff flow

1. **Request.** When `historyNeedsContinueAsNew` is true, set
   `childTracker.handoffRequested = true`. Checked (a) at the top-level loop
   boundary (as today), (b) at EVERY agent loop iteration boundary, including
   spawn loops (same `InlineLoopExecutor` code path), and (c) inside
   `awaitLiveDetachedSpawns`' Await predicate, so a parked main thread notices.
   Deterministic: `GetCurrentHistoryLength/Size` are replay-stable (set per
   WorkflowTaskStarted).
2. **Spawns park.** A spawn loop that sees `handoffRequested` at its iteration
   boundary (the same place the top-level loop checks — after the checkpoint,
   before any work) stops and returns a new sentinel `errSpawnHandoff`,
   recording `{toolCallID, childThread, childWorkflowID, preset, title,
   loopIteration}` into `childTracker.parkedSpawns`. `runSpawnInlineChild`
   treats `errSpawnHandoff` as "parked": no completion/failure status, no
   mailbox enqueue, no `completeDetachedSpawn`. Instead it marks the record
   parked (still counted as live for the UI, but not blocking the handoff).
3. **Main thread.** When every live spawn is parked (or there are none) AND
   the main thread is at its boundary (or parked in awaitLiveDetachedSpawns),
   emit CAN with `WorkflowInput.Resume` plus a new
   `Resume.Spawns []SpawnHandoff` carrying the parked records.
   - If the main thread is parked in `awaitLiveDetachedSpawns`, the resumed
     run re-enters the top-level loop at the recorded iteration with the
     spawns relaunched; the loop's normal flow re-parks on them. This is
     correct because the main thread had finished its turn (it was waiting,
     not mid-work).
4. **Successor.** Before entering the resume node, relaunch each
   `SpawnHandoff` as a detached spawn in RESUME mode: re-register in
   `liveDetachedSpawns` under the SAME toolCallID, reuse the same
   childWorkflowID/childThread (no new thread, no inject message — the thread
   already has its history), and start `runSpawnInlineChild` with a loop
   resume at the recorded iteration. On finish it enqueues to the parent
   mailbox exactly as before, so the parent sees one completion per spawn.
   Build this on the existing resumption machinery (`isResumption` in
   `parseSpawnToolCall`/`prepareSpawnInline` + `WithStartIteration`), NOT a
   parallel path.
5. **Blocking-wait budget.** A spawn mid-iteration finishes its current
   iteration before parking (an iteration is bounded: one CallLLM + one
   ExecuteTools). A spawn itself parked on ITS OWN children or on an
   ask_question/approval is at a boundary and parks immediately.
6. **Hard backstop.** Past `continueAsNewHardThreshold` (46 MB / 48k events),
   stop waiting: park every spawn at the next boundary it reaches and hand
   off; if a spawn has not reached a boundary within that headroom, cancel its
   in-flight activity (the step re-runs in the successor from thread history —
   ExecuteTools terminal idempotency and CallLLM delta identity make that safe)
   and carry it anyway.
7. **Pause armed** still blocks CAN (a resume signal must not be dropped).

### What stays out of scope

Signals in flight during the CAN window remain the accepted residual risk
documented in continue_as_new.go (all channels are reconstructible from
Postgres). `spawn_send`/`cancel_thread` to a parked spawn: the mailbox row is
durable and is delivered when the relaunched spawn's next CallLLM drains it;
a cancel for a parked spawn is re-checked in the successor via the
tool-call-id-keyed pause controller (must be verified in tests).

## Tests (Temporal testsuite; fail-first)

- Recovery: TERMINATED with reason "Workflow history size exceeds limit." at
  38k events → `ErrHistoryLimitExceeded`, no reset call; count-limit same;
  unrelated terminate → reset allowed. Reconciler path likewise.
- CAN with a live spawn: history forced over threshold (inject via a test
  seam for `historyNeedsContinueAsNew`) while a background spawn is mid-loop →
  spawn parks at its boundary, CAN emitted with one SpawnHandoff, successor
  relaunches it on the same thread/tool call, spawn completes, parent receives
  exactly one mailbox completion, tool_calls row ends completed (never stuck
  backgrounded).
- Main thread parked in awaitLiveDetachedSpawns when threshold crossed →
  handoff still happens (check (c)).
- Hard backstop path.
- Pause armed → no CAN.
- Spawn cancel issued while parked → honored in successor.
- Replay fixtures regenerated (DB on 55434) and green.
