# Immutable review authority — disabled follow-up

Stack base: reviewed `2dcef942b1ba12fd47a735cd045472d61d082904` / draft #1712. This slice adds no app registration, publisher implementation, public YAML fields, activation, gates, merge or release.

## Contract

`prreviewwait.New(Deps{Store, Continuity, Signals, Policy: AuthorityPolicy})` requires all dependencies. Nil policy fails closed. `AuthorityPolicy.Resolve(ctx, AuthoritySubject{OwnerID, RepositoryID, PR, HeadSHA})` returns `Authority{ID, PolicyRevision}`. The subject is derived from the authoritative stored binding, not node arguments, provider reference text or caller-selected source names.

Both Arm and internal Publish resolve the same policy. `Outcome` now requires authenticated server `OwnerID` and `AuthorityID`. The producer's monotonic `Sequence` orders outcomes only within the selected authority; it is not a policy revision. Publisher487's server assignment producer should use its registered policy authority ID and assignment/review revision, never fabricate GitHub review IDs. The existing unwired GitHub-review adapter requires its own explicit authority namespace and cannot mix with server assignments on one head.

## Persistence

Append-only task-created migration `20261027070025_pr_review_wait_authority`: new immutable binding/head authority row, bounded authority ID and positive policy revision; outcome authority ID column. Prior published stack migrations are untouched. Generation uses the existing Forge pin; no generated output was hand edited.

`Arm` and `Put` execute authority enrollment/check in the same Serializable transaction as their subscription/inbox writes. They reread binding owner and current head. Pre-Arm outcomes enroll only the trusted resolver's authority, not first-caller choice. Same-head ID or policy revision changes are refused. New head can select new policy. Blank legacy draft inbox authority is neither inferred nor authorized. Reads and consumption isolate the persisted authority namespace.

External policy resolution is intentionally outside the DB transaction. This protects against wrong-head/owner writes, but is **not instant policy revocation**: a policy may change after resolution and before commit. A production review_policies SQL adapter can join an atomic policy check later; this slice does not claim to solve that external-resolver race.

## Tests

Focused real SQL tests cover GitHub authority sequence999 failing before it can suppress server sequence2, pre-Arm policy enrollment, replay/out-of-order ordering, immutable same-head policy ID/revision, new-head selection, concurrent Arm+Publish, foreign owner, stale head, and head advancement during external policy resolution. Existing real Temporal+SQL wait regressions receive explicit test policy fixtures and still pass. Nil-policy constructor test fails closed.

## Holds

All prior activation holds remain: production policy/producer/app activity registration, retention/cleanup policy, full graph continue-as-new replay, and provider post-write reconciliation. Task allocation succeeded but post-create migration-order validation flags older draft-stack migrations below current origin/main; integration must re-version unpublished drafts using pinned commands before merge. No arbitrary retention or migration timestamps were invented.
