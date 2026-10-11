# Frozen: the integration action approval prompt (recorded before its removal)

`action_approval.json` is `fixtures/action_approval.json` byte for byte at
`origin/main` `14f6aff7`, recorded by `TestGenerateFixture_ActionApproval`
(#552): an attended turn calls `http__request`, a mutating integration action,
and its call_llm output lists it in `ToolCapabilities.approval_required`. So the
batch first asks — `ApprovalCreate`, the approval's timeout timer,
`signal.approval.<id>` — the person denies it, the timer is cancelled, and
`ExecuteTools` runs with the call in `refused_tool_calls` before the next turn
completes the run.

#664 removed the prompt: a batch now starts at once. A run that recorded the
prompt has no marker, so `actionApprovalRemovedChangeID`
(`runtime/action_approval_legacy.go`) answers `DefaultVersion` and it replays
the commands above — and a run still waiting on such an approval when it is
picked up keeps waiting and answers the call the way it always did. The gate
is consulted only for a batch whose recorded capability set requires approval,
which nothing writes any more, so no other history ever records its marker.
Without the gate this history fails with TMPRL1100 at event 57, the timer the
new code no longer starts (as does `frozen/2026-10-08-preflight-before-started/
action_approval.json`, the same prompt recorded with the older startup order).

The prompt shipped to prod in #552 (`b7a4d6bd`, part of prod build
`4c6f6d3e`), so such runs can exist there.

Retire this set together with `actionApprovalRemovedChangeID`, the
`answeredByItsRun` check in `grpc/services/approval.go`, and the deprecated
`ExecuteToolsArgs.refused_tool_calls` (7) and `ToolCapabilities.approval_required`
(12) — reserve both tags then — once no run recorded before #664's deploy can
still be open.
