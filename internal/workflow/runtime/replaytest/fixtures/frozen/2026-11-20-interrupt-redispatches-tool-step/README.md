# Frozen: an interrupt that re-dispatched the tool batch it cancelled (recorded before interrupt-settles-tool-step)

`interrupt_redispatches_tool_step.json` was recorded on `81983b55` (main before
the change) by `TestGenerateFixture_InterruptDuringToolBatch`
(`generate_interrupt_gen_test.go`). A `builtin://agent` turn calls `fetch` (held
running by the scenario's executor) and `manage_machine`, an activation call:
the workflow runs `AutomationApprovalPreview` for it (event 49), then
`ExecuteTools` (55). The user queues a message (`thread_wake`, 56) and
interrupts the thread through the production `threads.InterruptThread`
(`interrupt_thread`, 67), which cancels `ExecuteTools` (71). The old cancel
handler treated that like a pause and dispatched the same step again: a second
`AutomationApprovalPreview` (72), a second `ExecuteTools` (80), then the
`CallLLM` (88) that delivers the message. That is prod chat 17037094
(2026-11-20), where the re-run preview hung and the message was never
delivered.

After the change an interrupt settles the batch instead: one `ExecuteTools`
with `InterruptedTurn` set, no preview, no card. What keeps this replaying:
`interruptSettlesToolStepChangeID` (`interrupt-settles-tool-step`,
`internal/workflow/runtime/interrupt_settle.go`), asked only when an interrupt
cancelled an `execute_tools` step. This history has no marker, so it answers
`DefaultVersion` and re-dispatches the step as it was recorded. Verified to
fail replay (TMPRL1100) when the gate is removed.

Retire this set only together with the gate, once no run interrupted before the
gate's deploy can still be open.
