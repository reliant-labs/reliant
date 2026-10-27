# PR author handoff — disabled first slice

Task bf44f6c9-c8e2-48db-9583-d9e681de783a. Base: ce5a3df288387dfe0221c4ae8a2ef188cd4eae91. This implements the owner-approved narrow registry/router choice from `pr-author-handoff.md`; it does **not** activate a repair pipeline.

## Implemented

- Additive, future-only publication, binding and review-reference tables, projected by the project's pinned Forge generation. No backlog enrollment.
- `internal/prhandoff/{contract.go,service.go}` verifies provider repository ID/open PR/full source SHA and joins successful publication to authenticated PR-create receipt. Original root/execution/chat/worktree ownership is immutable.
- `internal/store/pr_handoff_binding_repo_ext.go` persists the contract. Serializable publication refresh rejects wrong predecessors and rolls back conflicting receipts, recognizes exact replay, and never moves ownership. Enrollment rechecks publication and existing binding head. Review identity deduplication and a transaction-time stale-head check are durable.
- `internal/prhandoffadapter/authors.go` resolves real activity context + persisted root/chat/worktree and describes the **exact** Temporal execution. A completed/cancelled/deleted execution is unavailable; a read outage is retryable, not proof of completion.
- `internal/prhandoffadapter/receipts.go` is an optional authenticated integration runner decorator; only PR creation is intercepted. Neither PR body nor trailer identity is read. Successful provider result is preserved on enrollment failure, with no mutation retry. Ambiguous provider timeouts do not enroll.
- `internal/prhandoffadapter/github.go` uses existing authenticated read actions for repository/branch/PR/review facts. Reviews require an explicit authorized stable reviewer-ID policy; no permissive default. Review text is never persisted; only bounded metadata/provider reference is retained.
- `internal/workflow/runtime/activities/handlers/git_publish_handoff.go` and the small sibling hook record successful publication after push, using provider-observed predecessor for CAS. Setter is not called by existing app providers. Ambiguous/no-op pushes cannot create ownership claims.
- Review results are `stale`, `needs_human`, or `pending_wait`. The last explicitly means **nothing was delivered**. No replacement agent, completed-run reopen, close-PR or merge action exists.

## Not wired; activation blockers

Nothing constructs these adapters in app roots or calls `WithPRHandoff`. This is disabled by construction, not an environment toggle that could accidentally enable incomplete behavior. Existing Chat A automation, launcher and trigger admission are unchanged. This work does not edit concurrency693's launch/proto/admission files.

Before activation: durable author wait/subscription with lost-wake protection; authenticated repo-bound review/check event adapter and reviewer policy; exact-execution idempotent message delivery (existing SignalRun is insufficient); provider timeout/post-write reconciliation; lifecycle and cleanup ownership. The future wait adapter should reuse concurrency693's approved `Spec.OnStarted` seam rather than add another launch hook.

**Retention remains an explicit activation blocker.** Owner chose metadata/provider references only for this disabled slice; no invented expiry duration for binding authority. Worktree cleanup's 24-hour policy does not apply to PR bindings. A future PR-lifecycle + grace policy needs a separate owner decision before these records accumulate in production. There is no GC job or production write wiring here.

## Validation and generation provenance

Migration created with `task db:migration NAME=pr_author_handoff`; ORM/contracts generated with `task generate:forge`, using forge.yaml/go.mod pin `v0.1.47-0.20261027025742-2372776cf3d4`, `GOWORK=off`, `GOTOOLCHAIN=go1.27.3`. No generated code was edited by hand. Four existing mock files (chat, streaming, tools, reconciliation) receive only the pinned generator's import-alias/hash normalization; included rather than manually reverting generated output.

Final scoped checks after generation passed:

- `go test -short ./internal/prhandoff ./internal/prhandoffadapter` (0.034s / 0.100s).
- `go test -short -run '^TestPRHandoff' ./internal/store` (1.131s), against storetest's private migrated pgtest databases; no 5434 access.
- `go test -short -run 'TestGitPublish' ./internal/workflow/runtime/activities/handlers` (0.675s).
- `git diff --check`.

No whole-repository gate, release, activation or merge was run. Review parking is not implemented; event-before-park coverage asserts durable `pending_wait`, not a working wake protocol.

## Failure semantics

Provider changed head after publication/create: reject, never silently rebind. Wrong publication predecessor: transaction rollback, not successful no-op. Same author/execution/head replay: idempotent. Another execution in same chat: reject. Authorized review arriving before a park: persist `pending_wait`, no wake claim. Terminal/missing original author: `needs_human`, explicit reassignment required. Stale-head review: persisted stale; no repair launch.

PR-create timeout can leave a provider PR without enrollment. This slice fails closed and requires human reconciliation; it does not rediscover authorship from public prose. Publishing after a persisted receipt failure reports nonretryable `PRHandoffNeedsHuman`. Automated receipt reconciliation is follow-up work, not a loop that retries provider writes.
