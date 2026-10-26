# Durable worktree recovery: current-main lifecycle investigation

Task: 05b9a95c-3688-45d1-a0b8-9e69d05ed222. Read-only source investigation of isolated `reliant` HEAD `c88ea55a4b8de26f78ee03a7bcb8f3f4be3960e9`; no sibling checkout used. Research/code-search/forge skills supplied by harness. No runtime cleanup or implementation performed.

## Approved requirements (not open questions)

Automatically reclaim after **seven days of genuine inactivity**, including dirty/unpushed work, **only after verified durable remote preservation**. Never automatically expire backups. Provide an inbox restore action. Running workspaces and active conversations remain protected. Failed preservation retains originals. Existing local refs are NOT durable remote preservation.

## Executive conclusion

Reuse the existing daemon-side scanner, local snapshot/history discovery, two-phase server/daemon protocol, archive-fence retirement, quarantine, and conservative refusal behavior. Do not merely change an idle constant or invoke confirmed cleanup automatically: the current snapshot collapses index/worktree state, omits ignored real data, relies on the original machine/shared object store, and has no remote backup receipt or durable recovery catalog. The current inbox is a cleanup affordance, not a recovery catalog.

## Existing end-to-end paths

- `internal/worktreesweep/sweep.go`: leader sweep groups DB candidates by user/machine; `describe` maps project repos into checkout descriptions, excludes archived paths overlapping live workspaces, passes archive timestamp fence, and asks active rows to probe only when enabled and not kept. `settleBatch` sends reconcile, re-reads archive/fence before phase two. `apply` merges results and records directory deletion only on archived rows.
- `storage.go:171-219,323-434`: authenticated user's connected machine/held rows -> StartCleanup -> detached 30-minute run; renewable three-minute cleaning lease. First `worktree.snapshot_remove` saves/claims; server re-reads row/fence, records returned refs, then sends Remove=true with saved tree map. This is asynchronous request work, not a persisted backup job state machine. Metadata write failures must become hard barriers for a durable deletion protocol, rather than best-effort progress.
- `internal/toolexec/daemonruntime/cmd_worktree.go:2691`: SnapshotRemove command adapter -> shared Reclaimer. `code_context(SnapshotRemove)` verified this route through server `cleanBatch`/`send` and daemon handler.
- `internal/worktreereclaim/reclaim.go:388-450`: root containment/availability, in-process path lock plus cross-process row file lock; recover parked active checkout before lock, repair parent Git damage, refuse unknown archive fence. Active rows are locked, not deleted.
- Confirmed first phase (`753-817`) checks structure/process use, saves orphan tips, snapshots dirty/unpushed checkout, verifies, and claims archive fence. Second phase (`820-1054`) rescans, fresh process check, copies loose files, quarantines each checkout deepest-first, re-locks, checks structure/data/process use and saved tree on parked checkout, then Git removes it. Failure attempts move-back; occupied original path leaves a visible held quarantine. Workspace removal is per-checkout, not all-or-nothing.
- Automatic path (`613-703`) does NOT snapshot dirty files. It can save unpushed/unreachable commits locally via saveCommits and then reclassify. Held workspaces may have regenerable output shed (`strip.go`), subject to ownership and process checks. Current archived idle floor is one hour; active finished floor is 24 hours; active auto-archive defaults to dry-run (`sweep.go:126-134`). These are not the approved seven-day policy.

## Preservation fidelity and reusable pieces

### Working files and index

`Snapshot` (`snapshot.go:24-55`) uses temporary index, workTree, commit-tree parented at HEAD, create-only randomized refs, then rehash comparison. It neither changes HEAD nor real index nor files. `classify.go:69-78` seeds that index from HEAD and `git add -A`, excluding nested linked checkouts. Nonignored untracked files join the tree; untracked-size cap is 512 MiB. New/moved gitlinks are refused by checkGitlinks; an unchanged gitlink is not its repository's contents.

**Explicit fidelity gap:** `classify.go:104-107` documents loss of an intermediate staged blob when disk content differs from index. Restore leaves the real index at HEAD, so staged/unstaged distinction and original untracked status are not restored. Git tree hashing also is not a general filesystem byte archive (filters/attributes, metadata, empty directories, special files and index extensions/conflict stages require explicit handling). A durable format needs independently represented HEAD, index (including stages/flags or fail-closed unsupported state), working files, deletions, untracked inventory, and content checksums. Do not advertise the existing single tree as full workspace preservation.

### Local history

`backup.go:27-73` saveCommits keeps HEAD lacking a containing remote-tracking ref and orphan tips under deterministic `refs/reliant/wip/<id>/<checkout>/orphans/...`, with create-only/idempotent keepRef. `classify.go:434-544` orphanTips collects both HEAD reflog columns, private worktree/bisect refs, pseudo-refs (ORIG_HEAD/MERGE_HEAD/CHERRY_PICK_HEAD/REVERT_HEAD/REBASE_HEAD), detached HEAD; validates extant commit objects, subtracts local branches/remotes/tags/current snapshot namespace, and selects independent tips. Confirmed cleanup saves orphan refs plus snapshot parent history.

Reusable discovery is scoped to history endangered by removing this linked worktree. Subtracting local branches/tags is fine for same-clone deletion but **not enough for machine-loss recovery**. A remote artifact must carry the complete object closure of every promised recovery tip, not assume local refs or remote-tracking refs prove remote durability. Private reflog/pseudo-ref read failures are not uniformly surfaced (several os.ReadFile errors are ignored), and complete reflog/operation-state restoration is not implemented. Define recovery scope explicitly; include branch/HEAD identity and tip inventory in manifest, independently verify remote object closure.

### Ignored data versus rebuildable output

`data.go:30-56` has rebuildable allowlist (node_modules, dist/bin, tool caches, .forge render/log/state/tmp, .tools, root tmp, etc.) and never-list (data, env files, hostinfra, db/sqlite, secrets). dataWalk also exempts generated-marker files and source-checkout copies; dev secrets comparison permits value coverage rather than byte identity. Unknown ignored data holds cleanup. Nested checkouts are judged separately. These are conservative local-loss heuristics, **not proof of independent remote preservation**: a source copy can die with the same machine, and a generated marker is not a remote source guarantee. Keep unknown/special/unreadable content fail-closed. Real data/secrets need an approved encrypted remote policy, not forced Git staging. Existing scanner/allowlist can identify likely exclusions, but should not silently redefine complete backup coverage.

### Nested repositories and loose group files

`structureOf` (`reclaim.go:548-608`) accepts registered linked worktrees with matching ownership; main checkout/unmanaged standalone nested repository holds. Nested linked checkouts are excluded from parent's Git view and removed deepest-first. Arbitrary nested clones/submodules need separate history/files capture or continue to hold. No automatic destructive fallback is appropriate.

`rootfiles.go`: loose files outside checkouts are copied to same-machine `~/.reliant/reclaimed/<id>/...`, regular files/symlinks only, default 50 MiB total. copyRootFile uses temp+sync+rename and compares bytes; collision content gets a new suffix. removeOriginals re-compares and leaves changed originals. This is not remote and is not integrated into recreate restore. Result ReclaimedTo reports a local destination; no durable per-file recovery manifest is established by storage.go. Include group files/path layout in the same remote recovery record before deleting any checkout.

## Mutation, crash retry, fencing

- Reuse fresh tree rehashes, fresh process checks (errors hold), per-row/path serialization, create-only refs, exact fence lock claims, and quarantine origin markers written before move. `lock.go:105-157` retires old archive fences when restoring; old or non-newer claims cannot reclaim restored checkout. Missing root is foreign/unavailable, not evidence all work disappeared.
- Quarantine origin recovery runs on reclaimer initialization (`cmd_worktree.go:2625`) and restore (`1241`). Parent Git crash repair is separate from remotely durable backup.
- Snapshot tree rehash covers disk Git view, not original index, HEAD movement, new orphan tips, ignored data or all manifest entries. Extend final verification to the full inventory, after quiescence/quarantine, and bind receipt to worktree + archive generation + capture generation + manifest digest. Upload success alone cannot authorize delete.
- A running process with cwd inside is protected; rename discourages new old-path writers but is not a filesystem writer lock, and Git worktree locks are administrative locks. Open file descriptors, writers with cwd elsewhere and a process starting after enumeration remain races. Need an explicit activity/write admission boundary and/or repeatable full-content verification; do not claim rename alone freezes bytes.
- Partial multi-repo deletion, lost daemon response, upload-complete/DB-not-recorded, server death, restore death mid-apply, and retry after fence retirement require persistent idempotent phase state. Existing lease/backoff and local orphan refs are reusable mechanics, not such a remote recovery journal.

## Restore and recovery affordances

`code_context(ApplySnapshot)` verified callers: daemon recreateWorktree and `cmd_worktree_ensure.go` repairWorkspace (automatic missing-directory repair). `restore.go` applies only recorded wip refs (orphan refs excluded), selects latest resolvable snapshot, requires clean checkout and HEAD equal saved parent, writes via temporary index, rehashes. Branch movement returns ErrBranchMoved instead of reverting newer commits. Ordinary errors best-effort rollback to HEAD; process crash is not covered by deferred rollback. `ApplySnapshot` does not itself retire the ref despite its comment; recreate calls RetireAppliedRef separately, creating applied namespace then deleting old wip ref. This is not atomic/idempotent across all crash windows.

`handlers/worktree/worktree.go:1596-1738`: RecreateWorktree authorizes ownership, checks name conflicts, sends all project repo paths/branch/row refs/fence to owning daemon; restores all repo checkouts. Daemon requires branch availability (local or unique remote-tracking branch); cannot rebuild solely from orphan backup refs or fetch a remote artifact. Successful directory recreation unarchives worktree/chats even if snapshot apply failed; warnings preserve refs. Failed apply/branch movement keeps row refs; orphan refs remain listed even after successful apply. Restore currently requires original clone/machine and does not retrieve loose root files or ignored data.

Inbox: `handlers/inbox/inbox.go` dynamically builds per-machine storage items from StorageView, held rows and disk pressure; cleanup RPC starts above flow. `components/inbox/StorageAction.tsx:85-92,117-204` offers Free up space and Clean up, explains local-only refs/no push. Once removed, row no longer appears as held: this does not provide a retained recovery listing/action. `RestoreInboxItem` (`inbox.go:296-311`) only undoes dismissal, NOT workspace recovery. Existing web lifecycle helper is `frontends/web/src/hooks/worktree-actions.ts`; workspace status renders snapshot_refs in `components/Worktrees/workspaceStatus.ts`. Reuse worktree recreate/error presentation, but add explicit durable recovery records and a distinct inbox Restore action, including offline/wake/progress/failed/partial recovery outcomes. Never overload dismissal undo as restore.

## Active conversations and genuine inactivity

`archiveFinished` (`sweep.go:899-942`) refuses main/already-archived/keep-files rows and errors checking chats; GetLiveChatIDForWorktree (`store/chat_repo_ext.go:54-60`) means **any chat whose state is not archived**, not merely currently executing. This is strong existing protection worth retaining. It checks chat then archives in separate operations: concurrent chat creation/unarchive/start must be made atomic with reclaim admission, not merely rechecked before an asynchronous network call. Explicitly archived workspaces also need ongoing-conversation admission protection, not just the active-auto-archive entry point.

`describe` shared-path protection is per-user and lexical; live candidate read failure in ordinary sweep aborts, but confirmed `storage.go:312-315` substitutes nil live paths on error. Harden this to fail closed. Final cleanup phase also must revalidate new live-row overlap and activity generation, not rely solely on phase-one list.

`idleSince` (`reclaim.go:490-523`) uses workspace/checkout directory mtimes, admin HEAD mtime and actual reflog events (not reflog file rewrite/index refresh). Good anti-maintenance-noise primitive, but in-place file writes do not necessarily change directory mtime. It is not a seven-day genuine-inactivity ledger. Need authoritative user/chat/tool/process activity plus observed file mutations, with conservative unknown handling. Decide how unopened but unarchived conversations affect eligibility without weakening approved active-conversation protection.

## Implementation boundaries and owner decisions

1. Daemon boundary: complete capture + manifest, local mutation verification, transport adapter and verified remote readback; no server filesystem assumptions. Retain existing no-delete-on-uncertainty behavior.
2. Server boundary: durable backup/reclaim/restore state and owner scoping; seven-day activity eligibility; atomic activity/archive admission; remote receipt persistence before deletion authorization; durable catalog with no automatic expiry. Use existing routing, leases and fences, but do not treat CleanupMetadata.SnapshotRefs as a remote receipt.
3. API/UI boundary: explicit restore/recovery entry, per-repo/full-workspace outcomes and retry-safe status, inbox discoverability after directory deletion and source machine loss. Existing recreate remains useful only after fetching/importing the exact saved history/files/index.
4. Tests: staged-then-reverted blobs, conflict index/flags, filters/binary/symlinks, orphan and unpushed history, nested standalone/linked repos, ignored secrets/data, loose group files; mutation during capture/upload/quarantine; failed/mismatched remote verification; every crash boundary; new chat/tool start racing claim; restore on another machine with no original clone/branch; no backup expiry.

Unresolved architectural choices requiring owner approval before implementation:

- **Remote store and format:** encrypted object artifact + Git bundles/index/file manifests (recommended for arbitrary data and no Git-host assumptions) versus pushed Git refs plus separate non-Git archive. Trade-off: artifact reconstruction complexity versus multi-backend credentials/limits and incomplete Git-only coverage. Neither local refs nor ordinary branch pushes are sufficient.
- **Durability/security contract:** independently retrievable immutable object receipt/readback, encryption key custody, tenant authorization, secret-bearing backups, quotas and paid retention. No expiry is approved; storage exhaustion must hold originals, not erase old backups.
- **Admission/activity model:** authoritative generation/lease shared by chat/tool dispatch, archive and reclaim versus filesystem-only observation (insufficient for active-conversation protection). Recommend shared durable admission plus daemon process/full-inventory checks; cost is cross-cutting lifecycle wiring.
- **Recovery destination:** exact captured HEAD into isolated restored branch/path (recommended when original branch moved) versus refusing until manual resolution. Existing branch-moved refusal is safe but not a complete inbox recovery experience. Decide same-machine default versus portable machine choice and how name conflicts/partial restores are presented.
- **Scope:** full private Git operational state/conflict continuation versus explicit supported-state capture with unsupported states held. Do not silently lose staged blobs or operation state under a claim of complete recovery.

These choices are not implied approval by the seven-day policy. No implementation or deletion was started.
