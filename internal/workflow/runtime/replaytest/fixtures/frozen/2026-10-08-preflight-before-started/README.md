# Frozen: preflight before "started" (recorded before #641)

The fixtures as they stood at `fbca55fe^` — the last commit before #641
("wait for a waking machine instead of failing preflight"). Every run that
needs a daemon schedules `PreflightDaemonCheck` as activity 11 and the
"started" `WorkflowStatus` as activity 17.

#641 swapped the two, shipped without a version gate, and regenerated the
fixtures, so this shape stopped being tested. On the 2026-10-09 21:30 UTC prod
deploy every run still in flight with this shape failed its next workflow task
with TMPRL1100 (`history event is ActivityTaskScheduled: PreflightDaemonCheck
… replay command is ScheduleActivityTask: WorkflowStatus`). Chats 97654413 and
3f03dc31 retried that task for hours and showed as active the whole time.

What keeps this set replaying: `preflightAfterStartedChangeID` in
`internal/workflow/runtime/workflow.go`. A history with no
`preflight-after-started` marker runs the check before "started", as it was
recorded.

Not pinned, by design: histories recorded by #641's own build (2026-10-09
21:30 UTC to this gate's deploy). They carry no marker either and record the
new order, so they do not replay; the reconciler recovers a run of that shape
when it wedges. Supporting both marker-less orders at once is not possible —
nothing a workflow can read deterministically before the check tells them
apart (the #641 fixtures were recorded on 2026-10-08 14:08 UTC, before prod
chats that ran the old code).

Retire this set only together with the gate, once no run started before the
gate's deploy can still be open.
