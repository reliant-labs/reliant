# Frozen: a capacity wake that only reached usage-limit waits (recorded before capacity-wake-resumes-chat)

Every fixture in the current set at `a84e40ac` (main before the change), plus two
recorded on that same commit:

- `capacity_wake_ignored_while_paused.json`: a `builtin://agent` run whose
  first `CallLLM` is refused for spent credit (`insufficient_quota`, failed at
  once as `ProviderCreditExhaustion`). The root self-pauses (`WorkflowError`, a
  "paused" `WorkflowStatus`). `signal.usage_limit.retry` (now
  `CapacityWakeSignalName`) lands while it is paused (event 65), and the old
  run never reads it: no usage-limit wait ever started the listener. Then
  `signal.resume` (event 69) releases the pause, and the run retries and
  completes.
- `capacity_wake_ignored.json`: a sub-agent stops on spent credit and reports to
  its parent. The same wake lands while the parent reads the failure (event
  157), is again never read, and the agent stays stopped.

After the change a run listens for the wake from its start. Each wake also
releases a self-pause armed on a provider failure and resumes every sub-agent
that stopped on one (`internal/workflow/runtime/capacity_wake.go`). When the
paused history replays unversioned, the wake at event 65 releases the pause and
schedules the "started" `WorkflowStatus` before the history's `signal.resume`.
That is TMPRL1100 (`lookup failed for scheduledEventID to activityID:
scheduleEventID: 73`). This was verified by replacing the gate with `if true`.

What keeps these fixtures, and every other one here, replaying is
`capacityWakeChangeID` (`capacity-wake-resumes-chat`), asked once at run start.
These histories have no marker there, so they take the old path. In the
sub-agent history, resuming the agent is also behind
`message-resumes-stopped-subagent`, which that history never recorded.

Both were recorded with scratch generators on a detached `a84e40ac` worktree
(the pre-change `UsageLimitRetrySignalName` and `UsageLimitRetrySignal` names).

Retire this set only together with the gate, once no run started before the
gate's deploy can still be open.
