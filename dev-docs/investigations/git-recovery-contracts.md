# Git recovery contracts — frozen Unit A v1

Owner: Unit A (schema/proto/wire and sole generator). Other units request changes; do not edit these inputs/projections. Git-only, no TTL, no object storage. Existing clean/manual behavior is not globally replaced by this additive contract. The new recovery path always requires fresh remote proof.

## Concrete daemon API (`internal/worktreereclaim`)

All structs use snake_case JSON. `RecoveryCapture` contains `Version int`, `CaptureID`, `WorktreeID`, `RepoID`, `Rel`, `RemoteURL`, `RecoveryRef`, `CommitOID`, `ManifestDigest`, `HeadOID`, `IndexTreeOID`, `DiskTreeOID`, `ObjectFormat`, `InventoryDigest`, `SecretPolicy` strings, `HistoryTips []string`, `UntrackedPaths []string` (base64 raw paths), `CapturedAt time.Time`. No credentials or local paths in this durable descriptor. The manifest format/encoding is B-owned; descriptor digest names those canonical bytes. C and G consume B's manifest, never invent a second serializer.

`RecoveryReceipt { Capture RecoveryCapture; VerifiedAt time.Time; VerifierVersion string }` means direct exact-ref fetch in an independent empty object store, strict complete closure and manifest verification. Push success/ls-remote alone never produce it. Namespace: `refs/heads/reliant-recovery/<worktree UUID>/<capture UUID>/<repo opaque id>`. Absent-ref CAS create; exact match retry; any other SHA holds. Visibility is inherited from repository; no confidentiality claim.

`RecoveryActivity { Generation int64; ObservationID string; ObservedSince, LastActivity, ObservedThrough time.Time; Complete bool }`. Gaps/restart/overflow reset the observation epoch. Server generation and daemon observation ID are independent and both fence deletion.

`RecoveryAuthorization { RecoveryID, CaptureID, WorktreeID, Fence, ManifestDigest, ObservationID string; ActivityGeneration int64; ExpiresAt time.Time }`. ManifestDigest is the workspace digest, not an individual repo digest. Exact equality only. No old `Remove` boolean grants recovery authority. `RecoveryRequest { Worktree Worktree; RecoveryID string; Captures []RecoveryCapture; Receipts []RecoveryReceipt; Activity RecoveryActivity; Authorization *RecoveryAuthorization }`; `RecoveryResponse { Outcome Outcome; Reason HeldReason; Detail string; Captures []RecoveryCapture; Receipts []RecoveryReceipt; Activity RecoveryActivity; RemovedRepoIDs []string }`.

`RecoveryRestoreRequest { RecoveryID, IdempotencyKey, DestinationPath, Branch string; Captures []RecoveryCapture }`, `RecoveryRestoreResponse { Outcome Outcome; Reason HeldReason; Detail, Path, Branch string; RestoredRepoIDs []string }`. G creates a separate destination and restores HEAD/index/raw disk without filters; existing branches and source workspace stay untouched. C publishes/verifies/deletes one concrete capture at a time; API functions remain concrete (context plus descriptor, credential stays request-scoped in daemon command envelope), not a universal backup callback framework.

## Durable DB API (`internal/db`, core aliases)

`WorktreeRecovery` stores ID/OwnerUserID/SourceWorktreeID/ProjectID/DaemonID, CaptureID, Fence, ActivityGeneration, ObservationID, ManifestDigest, Phase, LastError, IdempotencyKey, timestamps and `Checkouts []RecoveryCheckout`. Source IDs are retained strings with **no cascading FKs**. `RecoveryCheckout` stores RepoID/Rel/RemoteURL/RecoveryRef/CommitOID/ManifestDigest, descriptor JSON, receipt JSON, Phase/LastError. No file bytes, credentials, TTL or automatic ref pruning.

Methods on Repo:
- `CreateWorktreeRecovery(ctx, *WorktreeRecovery) error` records immutable per-checkout capture/upload intent idempotently (owner + key); validates owner via source project.
- `GetWorktreeRecovery(ctx, ownerID, recoveryID) (*WorktreeRecovery,error)`; `ListWorktreeRecoveries(ctx, ownerID, projectID string) ([]*WorktreeRecovery,error)` (empty project = all).
- `ArchiveWorktreeForRecovery(ctx, ownerID, worktreeID string, generation int64) error` atomically archives an active recovery candidate under parent lock with owner/generation/nonmain/no-live-chat/no-pending-active-paused-workflow checks. E consumes this for dirty/unpushed automatic enrollment; ordinary manual archive is unchanged. Daemon seven-day evidence still required before invoking.
- `ClaimWorktreeRecovery(ctx, ownerID, recoveryID string, generation int64) error` locks source worktree, verifies archive/fence/generation/no live chat, claims finalization. No network in transaction.
- `RecordWorktreeRecoveryReceipt(ctx, ownerID, recoveryID, repoID, receiptJSON string) error` records only exact descriptor matches; JSON is the above concrete receipt.
- `AuthorizeWorktreeRecovery(ctx, ownerID, recoveryID string, expiresAt time.Time) error` requires all receipts, current claim/fence/generation; journals authorization deadline. E forms the concrete authorization from persisted row.
- `CompleteWorktreeRecoveryCheckout(ctx, ownerID, recoveryID, repoID, phase, detail string) error` journals per-checkout `removed`, `restored`, `refs_deleted`, or `held`; aggregate completes only when every checkout matches.
- `CancelWorktreeRecovery(ctx, ownerID, recoveryID, detail string) error` holds journal and releases claim, retaining captures. Refuses while persisted authorization is unexpired (an in-flight daemon cannot be revoked by a DB boolean); after that exact deadline E reconciles quarantine/outcomes then cancels. Authorization expiry is NOT backup TTL. Removed/restored/ref-deleted and active restore/delete operations are not overwritten by cancellation.
- `BeginWorktreeRecoveryOperation(ctx, ownerID, recoveryID, operation, idempotencyKey, targetDaemonID string) error`: operation `restoring` or `deletion_requested`; restore requires target, delete requires empty target. Persist Operation + OperationKey + TargetDaemonID before remote work. Same op/key/target resumes held; same-key changed target/op or opposite held operation rejects. Terminal same-key retry is idempotent. G does remote work after this transaction. (Coordinated extension approved by parent.)
- `RecordWorktreeActivity(ctx, worktreeID string) error` increments generation and invalidates claim under the parent row lock; retries allowed, never decrements.

All chat create/save/rebind/unarchive and worktree unarchive share parent-row locking with claim. During finalization, new live binding/activity is rejected with `core.ErrWorktreeFinalizing` (retryable), not silently admitted. Archive and claim are separate: claim checks bound nonarchived chats atomically. Runtime E/D must use activity admission too; DB does not pretend a chat check proves no processes.

## Public API (`worktree.proto`, `inbox.proto`)

Worktree RPCs: `ListWorktreeRecoveries`, `RestoreWorktreeRecovery`, `DeleteWorktreeRecovery`. Requests are owner-authenticated; list has optional project/source worktree filters; restore/delete require recovery ID and idempotency key, restore requires target daemon ID. Responses include recovery catalog entry; restore also new worktree ID. `WorktreeRecovery` / `WorktreeRecoveryCheckout` public messages expose identifiers, phases, exact remote ref/OID, errors, timestamps; never descriptor credential material. Inbox adds `INBOX_ITEM_KIND_RECOVERY` and `RecoveryInboxItem` payload (recovery ID, source/project labels, phase, checkout count/error), independent from source row or connected machine. `RestoreInboxItem` stays undismiss-only.

## Conservative boundaries

Seven days gates automatic dirty/unpushed reclamation only, with complete genuine observation; manual user initiation does not bypass preservation/active-use gates. Unsupported history/index/secret/unknown ignored data stays. No schema or wire type makes a scanner, watcher, bare-remote test or GitHub transport capability proven. Keep destructive rollout disabled until integration and disposable actual-host proof. No automatic deletion of catalog/refs on source deletion or restore.
