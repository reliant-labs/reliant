# Frozen: a sub-agent's exhausted step pauses the whole run (recorded before isolation)

`spawn_failure.json`, recorded against the runtime at `78bfc941` (origin/main
before sub-agent failure isolation) by the `TestGenerateFixture_SpawnFailure`
scenario in its pre-isolation form: the parent spawns a sub-agent, the
sub-agent's `CallLLM` fails, and its exhausted step self-pauses the shared
pause coordinator — `WorkflowError`, the child's `WorkflowStatus` "paused",
the `ordered-scope-cancel` marker of the run-wide cancel — and the whole run
parks. A `signal.resume` (the scenario sent it once the child's row read
paused, after letting the scripted driver succeed) re-dispatches the
sub-agent's `CallLLM`, which completes; the parent reacts to the completion
and the run finishes. The scenario script was the current one plus a fourth
turn for the child's post-resume reply, and a resume step.

This is the shape every chat that parked behind a sub-agent before the change
is sitting in — the 2026-10-10 orchestrator run that held for 70 minutes is
one. What keeps it replaying: `subAgentFailsAloneChangeID`
(`subagent-exhaustion-fails-agent`, `internal/workflow/runtime/subagent_failure.go`).
`failsAlone` consults it at the exhaustion point on a sub-agent's thread; a
history with no marker there takes the pause path it recorded, and stays on it
for the rest of that execution — so a resume re-dispatches the step exactly as
before. Verified both ways: this history replays with the gate, and with the
gate removed it fails `TMPRL1100` (`lookup failed for scheduledEventID to
activityID`).

Retire this set only together with the gate, once no run that parked behind a
sub-agent before the gate's deploy can still be open.
