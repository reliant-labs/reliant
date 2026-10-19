# Frozen: a provider usage limit stops the agent or pauses the run (recorded before the wait)

Every fixture in the current set at the commit before usage-limit waiting.
What it pins is `spawn_failure_resumed_by_message.json`: the spawned agent's
first `CallLLM` hits a provider usage limit (`ProviderUsageLimit`, non-retryable)
and the step exhausts at once. Before the change that ended the agent
(`subagent-exhaustion-fails-agent`: a thread-scoped `WorkflowError`, the failed
report into the parent's mailbox) — or, on the root thread, self-paused the run
(`WorkflowError`, `WorkflowStatus` "paused", a park on the pause coordinator
until `signal.resume`). Neither schedules a `UsageLimitWait` activity or a timer.

After the change the same failure writes a wait marker, parks on a durable
timer until the reset and retries the step (`usage_limit_wait.json`).

What keeps these replaying: `usageLimitWaitChangeID` (`usage-limit-wait`,
`internal/workflow/runtime/usage_limit_wait.go`). `usageLimitWaiter.wait` calls
`workflow.GetVersion` at the one point the shapes first differ — a retry-
exhausted step whose error is a `KindProviderUsageLimit` marker — and a history
with no marker there falls through to the stop/pause path it recorded, and stays
on it for the rest of that execution. The other fixtures in this set never meet
a usage limit; they are here because the change also adds a signal coroutine
that only starts on the first wait, and this set is the proof a run that never
waits has the command stream it had.

Retire this set only together with the gate, once no run that stopped or paused
on a usage limit before the gate's deploy can still be open.
