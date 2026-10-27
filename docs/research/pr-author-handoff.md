# Original-author review handoff — focused architecture briefing

Task: bf44f6c9-c8e2-48db-9583-d9e681de783a. Read-only implementation investigation; no backlog changes, activation, gates, release, or PR.

Source: Reliant `origin/main` as checked out at **ce5a3df288387dfe0221c4ae8a2ef188cd4eae91**. Isolated worktree `pr-author-handoff-research`; only this document is written. Recommendations below require owner approval, not an implementation mandate.

## Executive answer

Existing primitives can persist a message and wake a **live paused original author** without polling: authenticated webhook intake → server-validated PR/author association → `SignalRun` / `Runs.DeliverToRun` → existing chat message delivery and Temporal resume. The original author must deliberately remain live and parked after publishing. `send_to_run` explicitly does **not** restart completed runs. Completed, cancelled, inaccessible, ambiguous or missing authors must become explicit needs-human/reassignment cases, not new implementer launches.

The write ledger supplies useful association evidence, but is an echo/loop-suppression ledger, **not yet an authoritative durable PR-owner registry**. Its origin enrichment must not be repurposed unchanged as an authorization decision. Small targeted server-side glue and persisted routing/idempotency state are missing; a prompt-only workflow is insufficient.

## Persisted association evidence

* `internal/workflow/runtime/activities/handlers/git_publish.go`: `GitPublishActivity.Execute`, `beginPush`, `runTrailerValue`. Runtime supplies `httpaction.CallSite{RunID: rtx.WorkflowID, NodeID: rtx.StepID}` and push attribution, not a PR-body run ID.
* `internal/integrations/writeledger/push.go`: `Recorder.BeginPush`, `PushIntent.Settle`. Before pushing, records GitHub scope, branch and head SHA; when PR number is known, records a second PR-targeted synchronize row. Ledger failure prevents push when configured. Nil ledger is explicitly permitted, so association cannot be assumed universal.
* `internal/integrations/writeledger/recorder.go`: `recorded.RunAuthenticated` records mutating integration call intent before provider execution; `settle` persists successful returned object ID/URL; `attributeRun` resolves workflow to chat, owner and trigger lineage. Attribution is best effort. Importantly, persisted `ChatID` is not necessarily an exact root workflow execution ID.
* `internal/integrations/writeledger/plan.go`: `planFor` includes PR creation and update; `plan.completion` resolves provider response into target/object ID. PR-create receipt and corresponding publish evidence are better seeds than any public prose or trailer.
* `internal/integrations/writeledger/contract.go`: retention is **30 days**. Ownership needed beyond that cannot depend on ledger survival.
* `internal/triggers/origin.go`: `OriginEnricher.Enrich`, `match`, `fromWrite`, `intentMatches`. Exact, intent, app and claimed confidence tiers were designed for suppression. Trailer claims have no run identity. Same-owner filtering protects disclosed run identity. Intent matching tolerates absent fields and SHA prefixes; exact lookup checks event compatibility, not a full PR-repair authorization contract. Review submissions describe the reviewer’s write, not the PR author's creation: `trigger.origin.run_id` is not a universal author lookup.

Recommended association key: authenticated integration/installation scope + canonical repository identity + PR number; value: owner, exact original root execution ID, chat/session ID, worktree identity and current published head repo/ref/full SHA, backed by successful create/push receipts. Reject incomplete/ambiguous historical evidence rather than inventing ownership. This is future-pipeline enrollment, not backlog migration.

## Delivery, resume and ownership

* `internal/llm/tools/send_to_run.go`: `sendToRunTool.Execute` resolves caller/owned target, rejects self, empty messages, pending/non-live roots; returns `delivered:false` for finished runs. Delivery receipt is persistence plus nudge, not acknowledgement.
* `internal/agentruns/agentruns.go`: `Runs.DeliverToRun` invokes `SignalRun` under owner context, returns delivered/message ID. Authoritative `code_context(DeliverToRun)` resolved this adapter and the tool relationship.
* `internal/handlers/run/run.go:254–298`: `Service.SignalRun` calls `runOwnedBy`, rejects non-live/pending status, chooses target thread and delegates `Chats.SendMessage`. No replacement run is created here.
* `internal/llm/tools/control_run.go`: `controlRunTool.Execute` exposes owner-scoped pause/resume/cancel of another top-level run. Pause is requested, not an immediate durable parked acknowledgement; an in-flight long call can still finish first.
* `internal/workflow/runtime/pause_coordinator.go`: `newPauseCoordinator`, `CheckPause`, `broadcastResume`: `signal.pause` / `signal.resume` and `workflow.Await` coordinate durable parking; explicit user pauses are distinguished from self-pauses and their retry ladder.
* `internal/workflow/runtime/workflow.go`: existing `ThreadWakeSignalName` channel, update-state signals; `question_flow.go` uses durable signal channels for question answers. `internal/triggers/workflowevent/{dispatch.go,match.go}` provides event-driven workflow dispatch; it is not by itself targeted author continuation.

Do not conflate chat ID, a root run ID, Temporal execution identity and a new turn in the same chat. A routing change must prove it addresses the enrolled original root, including completion/rollover races. Delivery must recheck liveness and classify unavailable distinctly from permission failure. The public receipt alone does not prove an atomic exact-execution delivery invariant.

## Authentication and event routing

`internal/integrations/webhook/github.go`: `GitHubProvider.Verify` requires the GitHub signature header and verifies HMAC before parsing. That authenticates delivery from GitHub, **not** authority of arbitrary comments or PR-body contents. Repository/installation authorization, reviewer or check-app authority, and exact source-head checks remain necessary at the routing boundary.

For every review/queue event: bind installation/owner/repository/PR; retrieve trusted provider facts; resolve association from server records; require event subject/source head to equal the enrolled current full SHA and head repository/ref. Merge-group synthetic commit SHA is not automatically the source PR SHA. Validate check producer / authorized review actor and event type. Ignore stale-head findings for repair; never let arbitrary issue comments select a run or instruct a privileged close. Persist delivery deduplication by provider delivery/event identity plus PR/head/review round. Treat text findings as untrusted task data, not authority.

## Recommended small change, subject to decision

1. At successful future publish + PR creation, persist authoritative PR→original-root binding using server call-site and receipts. Add exact execution identity (ledger currently principally attributes chat), lifecycle and head revision. Do not backfill from PR bodies.
2. Keep original author in a named awaiting-review phase, durably parked; avoid polling and fake long shell waits. Arm subscription/park before exposing review readiness, so fast events cannot be lost. Existing Temporal pause machinery is substrate, but a race-safe park-and-subscribe/acknowledged review-wait phase is not established by these reads.
3. Add a narrow deterministic intake router for authorized review and queue outcomes. Validate binding/head/producer, persist idempotent handoff, then call existing owner-scoped signal/message path. Do not spawn an implementer. Retry transient delivery errors durably; expose terminal undeliverability as needs-human.
4. Findings wake the enrolled author; author repairs and `git_publish` advances binding head. Require a new review of that exact head; an old pass does not authorize merge. Accepted/no-change review also needs an explicit next-state event so the waiting author can conclude.
5. Explicit needs-human with reason for completed/cancelled/missing/unavailable authors. Reassignment is an explicit owner action with recorded transition, never silent replacement.

Trade-off: reusing only current ledger + workflow prompts is smaller but cannot safely guarantee exact-root ownership, retention, event idempotency or lost-wake freedom. A narrow PR-handoff record/router is more code but makes those invariants testable. A generic subscription/event-bus redesign is broader than this task and not recommended without a demonstrated second consumer. Reopening completed roots would change lifecycle semantics; reject it for this proposal.

## Closing and cleanup

Author and reviewer should only close a PR when **superseded with proof**: authenticated repository-bound replacement identity, evidence it covers the old change, explicit allowed actor/action, persisted reason and source/replacement heads. Review rejection, terminal author, stale review or an arbitrary comment is not supersession. No proven general close-authorization primitive was established here; provider PR update capability is not policy enforcement.

`internal/worktreesweep/sweep.go` persists cleanup `SnapshotRefs` and held outcomes (`recordHold`); `internal/worktreesweep/recovery.go` persists capture/recovery-ref/commit-OID receipts and verifies remote recovery evidence before finalization. Reuse these references as cleanup/recoverability signals attached to the association where useful. They do not prove PR equivalence or supersession. A review-wait author must retain active ownership/lease protection; cleanup must not remove its worktree merely because publication happened.

## Remaining validation before implementation

Trace exact `SendMessage` pause-resume and run-rollover races, publish/create ordering and failure reconciliation, webhook installation/repository authorization, delivery dedupe retention, and worktree lease predicates in a design pass. Check whether merge-review/check event payload support covers all proposed events; this investigation does not certify it. Add focused replay tests for event-before-park, duplicate/out-of-order/stale-head events, cross-owner/cross-repo spoofing, provider timeout after successful create, cancelled/completed author, and cleanup while awaiting review.

Navigation caveat: `code_context` is bound to the main workspace, not this research checkout. `DeliverToRun` resolved authoritatively there; a subsequent `SignalRun` lookup failed in that workspace. Source claims above were verified against files in the pinned isolated checkout; do not treat the main-workspace graph as proof that every origin/main edge is identical.
