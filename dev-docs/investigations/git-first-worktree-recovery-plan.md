# Git-first durable worktree recovery — implementation/delegation plan

Task: `39e594c4-e326-4ebc-b9f8-db47f0210d0d`  
Source baseline: isolated `reliant` HEAD `c88ea55a4b8de26f78ee03a7bcb8f3f4be3960e9`.  
Status: **plan only; no implementation, remote writes, cleanup, migrations or tests executed.**

## 1. Settled scope and safety contract

The owner rejected separate object storage and approved the narrower **Git-only** design. This plan supersedes the object-storage alternatives/open decisions in `durable-worktree-recovery-lifecycle.md`; those are not prerequisites and must not be reopened. No GCS, bundles uploaded elsewhere, new storage credentials, universal backup platform, or automatic backup expiry.

After **seven days of evidenced genuine inactivity**, an eligible Reliant-owned linked-worktree workspace may be reclaimed, dirty or not, only after every checkout has a remotely retrievable Git recovery capture and the server has durably recorded the verified receipt. Preserve captured HEAD, unpublished/endangered commit history, staged content, unstaged content, and nonignored untracked content, including their distinction. Restore to a **separate worktree/path and new branch**, never reset or overwrite the original branch. Keep recovery refs after cleanup, restore, source-row deletion and machine loss until an explicit owner-authorized deletion request.

**Deletion is an AND, not a best-effort sequence:**

1. Owner/machine/path ownership known; not main, not kept, no live overlapping workspace.
2. No nonarchived chat binds the worktree; no executing run/tool/terminal/process uses it; new admissions excluded during finalization.
3. At least seven days of valid inactivity evidence, including filesystem changes, with no observation gaps.
4. All content/state falls within the supported Git-only envelope; no secret/unknown-data eligibility failure.
5. All checkout recovery branches exist at their exact expected object IDs, and a fresh independent remote fetch proves their complete promised object closure.
6. Receipt and per-checkout recovery catalog committed before removal authorization; authorization matches archive fence, activity generation, capture ID and manifest digest.
7. Fresh process/admission/structure checks and a full re-inventory of the quarantined checkout still match the receipt before each destructive removal.

Any false, unknown, timeout, incomplete enumeration, DB failure, changed content/ref, expired admission, unsupported host/credential/state or lost response means **keep originals with a durable actionable reason**. No user-confirmed cleanup button bypasses preservation, secret, active-use or ownership checks. Unknown content is not silently excluded, redacted, uploaded or deleted. Rebuildable ignored output may be omitted only by the tightened, explicit eligibility rules below.

Risk: **high consequence**, bounded scope. Correct behavior for unsupported work is a held workspace, not increased format/backend scope.

## 2. Verified seams, and what must change

Read together with the lifecycle investigation. Current source was read, not inferred from imagined services:

| Current seam | Reuse / correction |
|---|---|
| `internal/worktreesweep/sweep.go`: `describe`, `settleBatch`, `archiveFinished`, `apply`, `send` | Existing per-machine routing, fairness, leases, keep setting and archive fencing. Replace local-only removal authorization and check-then-archive chat race. `AutoArchiveDryRun` / 24h-finished semantics are not the approved 7-day lifecycle. |
| `internal/worktreesweep/storage.go`: `StartCleanup`, `cleanBatch` | Share one durable preservation/finalization engine between scheduled and confirmed cleanup. `runCleanupCollect` currently converts failure to read live paths into `live=nil`; this must become a hard hold. Metadata writes currently best-effort cannot authorize removal. |
| `internal/worktreereclaim/reclaim.go`: `Reconcile`, `SnapshotRemove`, `settle`; `lock.go`, `rowlock*.go` | Reuse serialization, exact archive locks, retired-fence handling, quarantine and move-back. Both automatic local-history-only removal and confirmed local-snapshot-only removal must cease to be alternate destructive paths. |
| `snapshot.go`: `Snapshot`; `classify.go`: `workTree`, `orphanTips`; `backup.go`: `saveCommits` | Reuse plumbing/local pinning, but snapshot must represent real index separately from disk. Never infer remote durability from local refs or remote-tracking refs. Local branch/tag reachability is not enough to exclude history from a portable capture. |
| `data.go`, `rootfiles.go`, `strip.go`, `cwds_*.go` | Tighten heuristics for durable deletion. Same-machine loose-file copies, equality to source checkout and generated-marker files are not remote recovery proofs. Process CWD-only probes are insufficient. |
| `internal/toolexec/daemonruntime/cmd_worktree.go`: `handleWorktreeSnapshotRemove`, `recreateWorktree`; `cmd_worktree_ensure.go`: `repairWorkspace` | Existing command and recreation seams. Recovery-specific path must fetch exact saved capture and create a separate checkout, not invoke branch-DWIM restoration or retire recovery refs. Leave ordinary missing-directory repair distinct. |
| `internal/toolexec/daemonruntime/cmd_git_publish.go`: `isolatedGitEnv`, `pubRunGit`, `dangerousKey`, `handleGitPublish` | Reuse/extract hardened Git credential transport: explicit GitHub HTTPS URL, credential through inherited pipe, no hooks/redirects/config credential capture. Do **not** use `git.publish_prepare`: it stages/commits into the real checkout. Existing publishing is GitHub-only and refuses Windows. |
| `internal/gitcredentialclient/contract.go` | Existing control-plane token authority: `UserAccessToken`, `MintInstallationToken` with repository/permission narrowing. No new secret store. |
| `internal/store/chat_repo_ext.go`: `InsertChat`, `SaveChat`, `UpdateChatState`, `GetLiveChatIDForWorktree`; `worktree_repo_ext.go`: `ArchiveWorktree`, `UnarchiveWorktree`, `RemoveWorktree` | Live means **any nonarchived bound chat**, even idle. Put reclamation admission and chat binding/unarchive under the same row-lock protocol; avoid a handler-only check that another writer can bypass. |
| `internal/db/core/project_worktree.go`: `CleanupMetadata`, `Worktree.LastActive`; `store/worktree_repo_ext.go` | `SnapshotRefs` is local, not a remote receipt. `LastActive` plus directory mtimes is not a complete inactivity ledger. Add durable journal/catalog/admission state. |
| `internal/handlers/worktree/worktree.go`: `RecreateWorktree`; `handlers/inbox/inbox.go`; web `components/inbox/StorageAction.tsx`, `hooks/worktree-actions.ts` | Reuse authorization, destination machine handling and action conventions. Current `RestoreInboxItem` only undismisses; recovery needs an explicitly separate action and listing that survives source disappearance. |

`code_context` resolved `SnapshotRemove`, `ApplySnapshot`, and DB `ArchiveWorktree` relationships; some other symbol queries failed to resolve, so those locations were verified by direct source reads rather than treating missing tool output as proof of no callers.

**Current tooling differs from old memory:** `Taskfile.yml` is authoritative. It has `task test:affected`, `task ci:merge`, `task ci`, and pinned forge generation. `internal/db/testutil.go` uses forge **pgtest** template databases; it explicitly does not read `DATABASE_URL`. Do not point tests at the real-data 5434 stack or follow the obsolete shared-DSN instruction.

## 3. Git host and authorization contract

### Namespace: use real recovery branches, not arbitrary custom refs

Use one branch per checkout/capture, for example:

```
refs/heads/reliant-recovery/<opaque-worktree-id>/<capture-uuid>/<opaque-repo-id>
```

Generate/validate components as bounded UUID/opaque IDs with `git check-ref-format`; do not use collision-prone sanitized relative paths or expose chat/user names in ref names. All refs point to commits. Do not assume GitHub accepts `refs/reliant/*`; do not push tags, original branches, `--all`, `--mirror`, private refs, or configure broad push refspecs.

GitHub officially supports branches under `refs/heads/...` (Git references API create example). **That supports the namespace choice, not an assertion that every repository permits creation.** Rulesets can forbid branch creation/names, require signatures, block pushes for secrets/workflow changes or restrict deletion. Every capture must actually create or find its exact expected ref and independently fetch it. An unsupported/rejected policy holds the original, with no bypass or rule alteration.

Creation is compare-and-create: verify absent or exact same SHA, then use an explicit empty expected-old lease for that ref to prevent races (`--force-with-lease=<full-ref>:` is an absent-ref CAS, not permission to overwrite existing data). If the ref already has exactly the expected SHA, retry verification idempotently. Different SHA is a conflict/hold, never fast-forward/rewrite it. Do not rely on an earlier `ls-remote` alone for atomic non-overwrite. No multi-ref atomic-push dependency is needed: each repo has one self-contained capture commit; workspace-wide deletion waits for all repo receipts.

Recovery branches are **visible repository branches**, not hidden/private backups. Repository collaborators can read their contents; public repositories make them public. They count against host limits, appear in ordinary clone/fetch, and can trigger push automation. Add `[skip ci]` to synthetic commit messages as a courtesy only; it does not suppress all webhooks or workflows. Document this openly; never claim isolation/encryption or secret safety from the namespace. No fallback publication into another repository/fork if the intended remote refuses.

### Credential capabilities

- Resolve the explicit GitHub repository identity from registered repo metadata, cross-check daemon repo identity and the user's current authorization; never blindly trust a checkout's mutable push URL or URL rewrites. Credentials never travel in persisted journal/manifests, logs, Git config, argv or environment. Existing command payload/pipe handling is the reusable transport.
- Use the existing `gitcredentialclient` authority and existing owner-scoped provider connection. Prefer existing repository-scoped App mint capability where configured, requesting only required permissions; user-token transport remains the existing supported alternative, not a new PAT/secrets prompt. No installation token may bypass the owner's repository access.
- GitHub repository **Contents read** is needed for fetch/ref verification; **Contents write** for creating/deleting recovery branches. Actual Git operations are the final permission proof. Changes to `.github/workflows` in reachable unpublished history may require additional workflow permission or be rejected. Do not silently expand permissions or remove history to make a push work; hold with reconnect/permission detail. Current App permission-narrowing request documents contents/issues/pull_requests; do not assume it can mint a workflows-write token.
- Revoked/expired tokens, missing installation, read-only connection, denied repo, ruleset failure, rate limit or unsupported OS/host all hold originals. Refresh at call time, distinguish retryable failures from owner-action-required reasons. Restore requires read access; explicit ref deletion requires write/delete authorization and may separately be blocked by rulesets. No new control-plane token API is anticipated.

### Retention and explicit deletion

Never TTL, auto-expire, prune by age, retire-on-restore, or delete recovery refs as part of original branch deletion / workspace permanent deletion / normal cleanup. Catalog rows must survive original row/project/machine deletion; no cascading foreign key that erases recoverability metadata.

Explicit deletion identifies a recovery ID and **exact stored ref+SHA for every repo**, authenticates owner, warns that Git-host recovery may be lost, persists delete intent, and serializes against restore. Delete only if the remote still points to the expected SHA (exact expected-old lease for the deletion refspec). A changed ref is held for review, not deleted. Verify absence via an exact fresh remote lookup. Partial deletion stays visible per checkout and retries only the outstanding exact refs. Delete success does **not** claim cryptographic erasure: objects may remain in other refs, forks, clones and GitHub caches. If a collaborator deletes/mutates a ref or the remote repo is removed, mark recovery unavailable; no Git-only system can prevent that. Retention means Reliant does not expire refs, not provider-independent immutable storage.

### Limits and evidence

GitHub documents 100 MB enforced single-object and 2 GB enforced push limits, plus recommendations around repository/branch growth (5,000 branches). Apply conservative local object/total/operation budgets before any upload; budget/scan uncertainty holds rather than truncates history. Never silently add LFS or another backend.

Implementation acceptance includes an **opt-in disposable GitHub repository proof using the actual supported App/user credential class**: create recovery namespace branch, exact lookup, independent fetch/fsck, restore, explicit exact deletion. Test denied rulesets/read-only permissions and workflow-history rejection. No production repository is mutated merely to probe capability. This planning run performed documentation research, not an authenticated remote write; do not misreport a live GitHub test as already passed.

References read:
- https://docs.github.com/en/rest/git/refs — documented `refs/heads` create/read/delete operations. The rule “starts with refs and two slashes” is not proof of Git receive support for every arbitrary namespace.
- https://docs.github.com/en/repositories/creating-and-managing-repositories/repository-limits — object/push limits and branch growth.
- https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository — deleting references is not guaranteed erasure.

## 4. Supported capture format and eligibility

### Single self-contained Git commit per checkout

Extend the existing temporary-index/`commit-tree` seam. Do not mutate real HEAD, branch, index or worktree while capturing. A versioned synthetic **manifest commit** has:

- Parents reaching captured HEAD and each maximal required history tip. Carry HEAD/private reflog/per-worktree refs/detached history endangered by removal, including tips retained only by local branches/tags; do not subtract tips merely because local refs or remote-tracking refs contain them. For a bounded v1 implementation it is safe to include all successfully enumerated candidate tips rather than optimize away history. Pin local source refs until all dependent phases settle. Read failures hold.
- A tree containing manifest metadata plus separate `index/` and raw `disk/` trees. Index tree comes from an independently copied, validated real index (`write-tree`); disk tree is built from exact bytes and Git file modes of tracked and nonignored untracked files, using `hash-object --no-filters`/temporary index plumbing. The manifest records original HEAD/branch label, index-tree SHA, disk-tree SHA, untracked membership, modes, repo identity/relative path, history tip identities, format/object algorithm, worktree/fence/activity/capture IDs and scan-policy version. Index-only blobs must be reachable, not merely named in JSON.
- Canonical manifest encoding with a digest independent of the commit ID (no circular commit-hash field). Receipt stores manifest digest and capture commit SHA. Paths use unambiguous encodings for non-UTF8/newline filenames and NUL-safe Git enumeration; validate against absolute/`..`/duplicate/case collision paths. Bound counts, sizes, depth and parent fanout, holding rather than dropping entries.

Restoration sets HEAD to captured HEAD on a newly created recovery branch, reconstructs the saved index, then materializes the exact raw disk tree **without replacing that index**. This naturally recreates staged-versus-disk differences, tracked deletions and originally untracked files. Compare restored raw-byte/mode inventory, index tree, HEAD and reachable history to the manifest before marking success. Do not use checkout filters that transform bytes or execute helper programs during recovery.

### Supported v1 envelope (anything else holds)

Support ordinary complete Git histories and normal stage-0 indexes; regular files, executable bits and symlinks with their link text. Support registered nested **linked worktrees** only as independently captured checkouts with stable repo IDs and one workspace manifest mapping; exclude them from the parent inventory and require all receipts before removal. Never follow symlinks into outside content.

Hold on merge/rebase/cherry-pick/revert/bisect/sequencer operations, unmerged stages, intent-to-add/index flags or extensions the serializer cannot reproduce, sparse/split indexes unless explicitly proven, index/operation lockfiles, unborn/missing HEAD, shallow/partial/promisor or missing-object histories, grafts/replace semantics, filter/LFS-dependent content/history not independently representable, special files, unsupported filesystem fidelity needs, unenumerable reflogs/private refs, or truncation. Unchanged gitlinks do **not** prove submodule contents safe: initialized submodules/nested standalone repositories hold. Absent gitlink directories are allowed only if there is no omitted local content/state and the manifest does not promise submodule recovery.

Loose workspace-group files outside every checkout hold, even if current `rootfiles.go` could copy them locally. Unknown ignored data, env/key/credential/database paths hold. Rebuildable ignored directories may be omitted only by a strict owned allowlist with no secret/data exceptions or symlink escapes. Do not carry forward “generated marker,” “equal to local source checkout,” or dev-secret value-coverage heuristics as durable proof. No automatic `strip` mutation while eligibility is held or unknown.

### Secrets: preflight before any network egress

There is no existing comprehensive secret scanner in the inspected recovery/publish path. Add an explicit local publication gate; **GitHub push protection is not that gate** (bytes have already left the machine and protection is not universal).

Scan the complete candidate closure, not just the final diff: raw disk bytes, index-only blobs, all reachable unpublished/endangered-history blobs, relevant paths, commit messages and manifest text. Include staged-then-reverted secrets and secrets deleted in later commits. Sensitive-name checks plus a pinned content detector and policy-versioned results are required. Scanner absent, timeout, unsupported binary/encoding, ambiguous high-entropy/encrypted material, excessive budget, or inability to enumerate content means hold. No skip/redaction/override “push anyway” on the automatic path. Source-only history excluded as “already published” would require fresh proof against this exact remote, not cached tracking refs; initial implementation may scan everything instead.

A detector cannot mathematically recognize every arbitrary credential disguised as ordinary text. Do not advertise that as a proven guarantee. The enforceable contract is no automatic egress of detected or unassessable sensitive content, with conservative supported-content eligibility. If a proposed scanner/policy cannot meet that security bar for a class of data, leave that class ineligible rather than weaken the contract or add object storage. This limitation must appear in tests/docs, not be hidden behind “secret-free” booleans.

## 5. Remote proof, activity, and durable phases

### Independent remote proof

Push acceptance and `ls-remote` alone are insufficient. In a fresh isolated bare Git repository, **without alternates, object hardlinks, shallow/filter options, inherited object directories or source-clone objects**, fetch the exact recovery branch from the remote using existing authenticated transport. Require the fetched tip to equal expected commit SHA; parse/digest the manifest; validate parents/tree references, enumerate every promised tip/tree/blob and run connectivity/strict object validation. Use replacement refs disabled and sanitized Git config/environment. Recheck exact remote tip after verification. Record time/repo identity/ref/SHA/manifest digest and verifier/format version, not just “pushed=true.”

Before final removal, revalidate remote receipt while activity admission is held; a stale catalog entry is not permission to delete. All repos must be verified first. Cross-provider ref mutation/deletion after the final proof cannot be prevented without provider retention locks; that is an explicit Git-host limitation, not a reason to pretend local refs are durable.

### Genuine inactivity and active-use exclusion

Do not just set `IdleFloor=7d` on the old directory-mtime heuristic. Persist server activity generation/time and daemon observation generation/time. Seven days runs from the latest genuine activity/known-valid observation start, not archive time, cleanup attempts, Git index refresh or reflog file rewrite. Initial deployment, daemon downtime, watcher overflow, permission errors and observation gaps start a new safe observation period; never backdate from unknown history.

Track filesystem writes/creates/deletes/renames/attribute changes across the entire relevant checkout using a complete watcher plus periodic full-inventory reconciliation; watcher overflow or coverage/budget failure invalidates evidence. A dirty file edited twice must reset activity even while `git status --porcelain` stays identical. Existing `fs_watcher.go` hashes status/path output and cannot serve as this ledger. Reuse actual reflog-event logic in `reflog_activity.go`; do not count maintenance rewrites as user activity.

Server admission uses one worktree row-lock protocol shared by reclaim claim, chat create/rebind/unarchive, worktree unarchive and relevant runtime start. Any nonarchived bound chat blocks automatic reclamation; never autoarchive chats just to make a workspace eligible. Check active workflow/tool leases too, including archived-chat runs. A short DB transaction claims `finalizing` with activity generation/fence; **do not hold a DB transaction over Git/network I/O**. While finalizing, new activity must first cancel/retire the claim and receive daemon acknowledgement before using the original path, or await finalization and use the recovery/recreation path. A timed-out lease alone must not make a delayed old delete safe: explicit generation retirement/acknowledgement is required.

Daemon admission gates commands under a path and path-overlap closure, including the `handleDaemonCommand` dispatch, direct terminal input/creation and background exec lifetime. Keep read/write/exec admission held for the lifetime of the actual background process/session, not merely until the command returns. Control-plane lifecycle commands use a narrow explicit exemption, not arbitrary payload-supplied bypasses. Daemon restart invalidates outstanding finalization authority until it reconciles the journal.

Fresh process-use checks must include managed exec/terminal registries and supported OS inspection of CWD, open files and relevant executable/mapped paths; unreadable same-owner process state is uncertainty, not absence. Current Linux `ProcessCwds` silently skips readlink errors and needs tightening. Unsupported inspection platform holds deletion; Windows is already unsupported by the hardened publish transport. A rename/Git lock is not an OS write freeze. Re-inventory after quarantine and immediately before each remove; unexpected external writers/events abort and move back/hold. Do not claim perfect exclusion of arbitrary privileged hostile writers. If a platform cannot establish the supported quiescence contract, it remains ineligible.

### Persisted phases (proposed additions, not existing APIs)

Use dedicated recovery catalog/journal rows, not only `CleanupMetadata.SnapshotRefs`. Retain per-checkout progress, capture descriptor and exact expected refs before upload. Suggested phases:

```
observing/held -> capturing -> publishing -> verified -> delete_authorized
 -> removing -> removed

verified/removed -> restoring -> restored     (refs remain)
verified/removed/restored -> deletion_requested -> refs_deleted
```

Failures retain phase/last error and retry only idempotent steps. Store per-checkout remote/manifest/receipt and local-removal outcome; a workspace may be partially removed but is never displayed as fully removed/restored until every checkout succeeds. Record local pins/upload intent before network action, allowing discovery of an upload that succeeded just before a response/DB failure. All nondeterministic capture objects must be pinned/journaled before a push so retry does not mint an untraceable orphan recovery branch.

Receipt commit failure forbids delete. New activity invalidates delete authority, not the retained capture. Server/daemon death after authorization requires explicit recovery of exact operation/generation state; no replay of an old boolean `Remove`. Missing directory is not evidence of completed backup/removal; mark uncertainty and reconcile exact journal/quarantine origin. Multi-repo removal may be partial; require all remote receipts first and report/resume per checkout.

## 6. Delegation units — disjoint write ownership

All paths below are relative to **`reliant/`**. New files are explicitly marked. This is the future implementation graph; no subagents were spawned by this planning run. A freezes the contracts before the fan-out. Other agents never edit generated output or another unit's files; request changes through the owner. Each unit owns its matching new unit tests unless assigned otherwise.

### A — Schema, contracts, DB catalog/admission, and **sole generation owner**

**Own existing:** `internal/db/core/project_worktree.go`, `internal/db/repository_impl.go`, `internal/db/repo.go`, `internal/store/worktree_repo_ext.go`, `internal/store/chat_repo_ext.go`, `proto/reliant/v1/worktree.proto`, `proto/reliant/v1/inbox.proto`, `internal/worktreereclaim/types.go`. Own any required DB alias file (locate existing core aliases before adding one).

**Own new:** migration created via `make migration NAME=git_worktree_recovery` (use the emitted timestamp, never invent one); `internal/db/core/worktree_recovery.go`, `internal/db/repository_worktree_recovery.go`, `internal/store/worktree_recovery_repo_ext.go`, DB integration tests for catalog retention/admission.

Add worktree activity generation/admission and recovery catalog/per-checkout journal schema with noncascading source identifiers, owner/project/repo identity, format/capture/digest/receipt/phase/error fields and idempotency keys. Archive/activity and chat binding transitions serialize under a shared parent-row lock (`ReadCommitted` with explicit lock), not independent `GetLiveChat` and `Archive` calls. Expose narrow DB operations for claim/record/authorize/complete/cancel/list/restore/delete that return errors; no silent metadata merge barrier. Reuse `LastActive` where semantically correct but keep observed evidence distinct. Harden permanent source-row deletion to preserve catalog.

Extend worktree API with owner-scoped list/restore/delete recovery operations (source worktree ID optional after row deletion, capture ID required; target daemon and idempotency key for restore). Inbox adds a recovery item payload, not a reinterpretation of `RestoreInboxItem`. Daemon JSON wire types in `worktreereclaim/types.go` add capture descriptors, verified receipts, activity generation/authorization and new outcomes/held reasons; names introduced here are **new**, not claims existing interfaces exist. Freeze manifest/wire/state shapes from sections 4–5 in A's first commit, then generate. Do not add credentials to persistent structs.

**Generation owner:** A alone runs pinned `task generate:forge` / `make forge-generate-check`, the project-required forge lint after generation, and owns all resulting `internal/store/*_gen.go`, `gen/reliant/v1/**`, frontend generated protos/hooks and mocks. Later input changes route back to A, including final convergence generation. Do not hand-edit projections. Forward-only additive migration; if rollback behavior is broken by semantics, mark it honestly and disable old cleanup executors before rollout.

**Depends on:** none. **Outputs consumed by:** B/C/D/E/F/G/H.

### B — Supported Git capture, eligibility and pre-egress policy

**Own existing:** `internal/worktreereclaim/snapshot.go`, `classify.go`, `backup.go`, `data.go`, `gitrun.go`.

**Own new:** `internal/worktreereclaim/recovery_format.go`, `recovery_inventory.go`, `recovery_secrets.go` and matching tests.

Implement the one-commit format, raw disk/index separation, complete bounded history inventory, no-filter object writing, local capture pins, canonical digest, supported-state preflight and local secret gate. Tighten ignored-data policy and surface all inventory/history errors. Export only concrete descriptors/helpers needed by finalizer/restore/transport; callbacks/interfaces are consumed narrowly, not a new backup service hierarchy. Existing local snapshots remain readable for old manual repair, but cannot authorize new durable deletion.

Tests include staged A / disk B / HEAD C; staged secret then disk reverted; staged deletion/recreation; nonignored untracked binary/symlink/executable; stale remote-tracking history; orphan tips now only locally branched; ignored secret despite generated marker/copy match; operations/sparse/LFS/filter refusal; NUL-safe filenames; complete closure and preflight **zero remote calls** on any hold.

**Depends on:** A. **Parallel with:** C, D, H.

### C — Authenticated Git recovery transport and independent verifier

**Own existing:** `internal/toolexec/daemonruntime/cmd_git_publish.go` (only refactor transport primitives; preserve existing publish semantics/tests).

**Own new:** `internal/toolexec/daemonruntime/git_recovery_transport.go`, `cmd_git_recovery.go`, transport tests and disposable GitHub capability harness. Register recovery publish/verify/delete commands in the new file, not shared `cmd_worktree.go`.

Extract the existing isolated environment/pipe/helper/config rejection into reusable private helpers. Implement exact single-branch CAS create/idempotent match, fresh fetch/strict closure/manifest proof, exact remote lookup, and explicit expected-old branch deletion. Harden all read/write paths equally, disable hooks, redirects, replacement refs, inherited objects and filter execution. Wire transport with descriptor callbacks where needed; no change to real worktree/index. Use request-scoped credentials only. Handle stderr redaction and permission/ruleset/size/scan refusal classification without logging content/token.

**Depends on:** A (format/wire schema). Does not need B implementation to begin; use format fixtures and then cross-check with B. **Parallel with:** B, D, H.

### D — Genuine inactivity, daemon admission and process protection

**Own existing:** `internal/toolexec/daemonruntime/runtime.go`, `fs_watcher.go`, `cmd_exec.go`, `cmd_terminal.go`, `stream_terminal.go`; `internal/worktreereclaim/reflog_activity.go`, `cwds_linux.go`, `cwds_other.go`, `cwds_windows.go`.

**Own new:** `internal/toolexec/daemonruntime/worktree_activity.go`, `internal/worktreereclaim/activity.go`, `process_use_linux.go` (and fail-closed platform stubs), matching tests.

Implement durable observation state, complete watcher coverage/overflow reset, content-sensitive reconciliation, generation reporting, seven-day eligibility evidence and daemon path admission tied to actual process/session lifetime. Preserve current UI watcher behavior; it is not the recovery ledger. Add strict fresh process enumeration and admission tokens consumed by finalizer. Use existing managed process/terminal registries; do not invent same-machine server filesystem access. Keep observation/control traffic from extending activity artificially.

**Depends on:** A. **Parallel with:** B, C, H.

### E — Server preservation journal orchestration and composition

**Own existing:** all `internal/worktreesweep/*.go` except tests owned by another unit; `internal/app/providers_api.go`, `internal/app/compose.go`, `internal/handlers/worktree/service.go`, `internal/handlers/inbox/service.go` for dependency wiring only. Own corresponding API settings source only if existing auto-archive rollout controls need updating (locate `WorktreeAutoArchive` declaration first; do not add parallel config spelling).

**Own new:** `internal/worktreesweep/recovery.go`, `recovery_test.go`.

Route scheduled/confirmed removal through the persisted phases using A's DB operations and C/D protocol. Fetch current owner token through existing credential client/provider seam at operation time. Share one preservation engine; no detached goroutine-only journal. Distinguish held, retryable and permission failures. Seven-day eligibility applies to automatic reclamation; explicit cleanup may be user-initiated earlier but cannot bypass active-use/remote-preservation safety. Re-read and atomically authorize exact receipt/generation/fence, refresh live overlap checks at final admission and abort on lookup error. Persist all receipts before F removal request; old `Trees`/`SnapshotRefs` alone never suffice. Only report reclaimed bytes after verified remove outcomes. Disable automatic local-only commit-save deletion and held-workspace stripping paths, not just the new path's callers.

**Depends on:** A, C, D. **Parallel with:** F/G after their own dependencies. E alone handles shared composition edits, including G/H dependency requests.

### F — Receipt-bound quarantine and removal finalizer

**Own existing:** `internal/worktreereclaim/reclaim.go`, `lock.go`, `rootfiles.go`, `strip.go`, `internal/toolexec/daemonruntime/cmd_worktree.go` (all edits to this shared file, coordinated with G).

**Own new:** `internal/worktreereclaim/recovery_finalize.go`, matching tests.

Replace both old destructive authorization variants with exact receipt/generation/capture validation. Reuse row/path locks, archive-fence retirement, deepest-first quarantine and crash-origin repair. Obtain D admission, verify all remote receipts, rescan full supported inventory and process use after parking and immediately before each removal. No loose-file local-copy fallback or automatic destructive strip on held data. On any mismatch move back if safe, otherwise preserve visible held quarantine. Explicitly reject missing/legacy durable authorization at daemon boundary, including delayed old requests. Record durable per-checkout phase before/after destructive operation and return honest partial results.

F updates `cmd_worktree.go` registration/adapter and, at G's request, routes recovery-specific recreation descriptors to G's new helper. F never edits G's restore implementation or C's new transport file.

**Depends on:** B, C, D (and transitively A). E and F integrate through A's frozen wire types; neither edits the other's files.

### G — Separate-worktree recovery, no branch overwrite, explicit deletion handler

**Own existing:** `internal/worktreereclaim/restore.go`, `internal/handlers/worktree/worktree.go` (but dependency declarations remain E-owned).

**Own new:** `internal/worktreereclaim/recovery_restore.go`, `internal/toolexec/daemonruntime/cmd_worktree_recovery_restore.go`, `internal/handlers/worktree/recovery.go`, matching tests.

Implement list/restore/delete recovery RPC logic against A's catalog and C transport. Resolve target project/repo mapping on the target daemon, with a fresh clone when no parent clone exists. Fetch exact recorded recovery branch/closure, validate manifest and create an idempotently named **new Worktree row, path and recovery branch at saved HEAD**. Reuse safe registration/lock/recreate helpers; do not repurpose the original branch or reactivate source conversations before successful restore. Materialize disk/index faithfully with temporary staging and per-checkout journal. Conflicts never overwrite unrelated paths/branches. Resume partial restore; failure leaves visible recoverable state, not successful activation with snapshot warnings. Old ordinary recreate/ensure behavior must not consume or retire durable recovery refs. Request any shared `cmd_worktree.go` changes from F.

Explicit deletion authenticates catalog owner, serializes against restore, journals per-ref deletion and verifies absence; source hard-delete and successful restore do not call it. Retain tombstone/outcome for audit. No wildcard namespace deletion.

**Depends on:** B, C (and A). **Parallel with:** E/F/H.

### H — Retained inbox and client recovery actions

**Own existing:** `internal/handlers/inbox/inbox.go`; `frontends/web/src/components/inbox/StorageAction.tsx`, `InboxItem.tsx`, `InboxPage.tsx`; `frontends/web/src/hooks/inbox-queries.ts`, `worktree-actions.ts` and their hand-owned tests.

**Own new:** `frontends/web/src/components/inbox/RecoveryAction.tsx` and focused tests.

List catalog-backed recoveries independently of connected-machine storage/held rows, including source deleted/offline, ref unavailable and partial outcomes. Add restore-to-separate-worktree action with target machine selection and idempotency; show progress/retry and new workspace link only after complete success. Clearly distinguish restore from undismiss. Show structured hold reasons and never say local snapshots were “backed up remotely.” Add explicit permanent-recovery-deletion confirmation naming repository/ref retention implications. No auto-dismiss-on-source-deletion, hidden expiry or implicit delete-on-restore. Load web-styling before UI changes; reuse existing components, do not invent a visual redesign. Generated API hooks/protos are A-owned.

**Depends on:** A. Can use mocked generated contracts while backend implementation proceeds; integration joins at I. **Parallel with:** B/C/D and later E/F/G.

### I — Integration proofs, generation convergence and local merge gate

**Own new:** `internal/worktreesweep/recovery_integration_test.go`, `internal/toolexec/daemonruntime/recovery_lifecycle_test.go`, `dev-docs/testing-git-recovery.md`. Do not edit other units' fixtures/tests without handoff.

Build cross-boundary harness with real temporary Git repositories, disposable bare remote and isolated DBs; inject crash points/mutation hooks, not sleeps. Exercise all cases in section 7. Coordinate A's final regeneration after all proto/migration/Deps changes. Run scoped full package suites and the repo local merge gate, report exact SHA/results and known main-baseline failures separately. Live GitHub capability evidence remains explicitly opt-in and required before enabling destructive production rollout.

**Depends on:** E, F, G, H. Critical path: **A → max(B,C,D) → max(E,F,G) → I**. H runs from A in parallel; no artificial chain across independent Git, activity and UI work.

## 7. Required acceptance matrix

| Proof | Required assertion |
|---|---|
| Fidelity | HEAD C, staged A, disk B; index-only reverted blobs; tracked add/delete/rename; staged deletion with recreated untracked path; binary/symlink/modes; original untracked status. Exact raw bytes/index tree/history after fresh-machine restore. |
| History | Unpushed commits and reflog-only/private refs survive source clone/admin/reflog deletion; local branch/tag reachability does not exclude them; missing object/reflog errors hold. |
| Independent durability | Fake push success, wrong/missing ref, missing parent/blob, alternates contamination, stale remote-tracking refs, remote mutation after upload all fail closed. Verification repo contains no source-clone objects. |
| Non-egress | Dirty, staged-only, deleted-history, ignored and path-sensitive secret fixtures; missing scanner, unsupported binary, scan timeout/limit. Assert **no push/remote object upload** on preflight refusal. No content/token in logs or DB. |
| Narrow eligibility | Unknown ignored data, source-copy/generated-marker exception, loose group files, standalone nested repo, initialized submodule, sparse/conflict/filter/LFS/shallow/partial/operation states all retained with reason. Registered linked multi-repo capture works without treating gitlinks as content. |
| Inactivity | Exactly seven days boundary; repeated edits with unchanged porcelain status; process/terminal use; maintenance-only reflog/index changes; clock rollback, restarts, offline gaps, missed watcher/overflow/budget resets. No false deletion on unknown activity. |
| Admission races | New chat/create/rebind/unarchive and run/tool/terminal/background-exec start racing claim/quarantine/remove; overlapping live row; DB live-path query error; expired old lease/delayed delete; stale fence after unarchive. No active conversation loses files; transient admission refusal is retryable/recoverable. |
| Crash/retry | Before upload, after upload before DB receipt, after receipt before auth, after auth before park, parked before deletion, one repo removed, delete done/response lost, restore tree/index half-applied, server/daemon restart. Stable IDs/refs and per-checkout progress; no false “removed/restored.” |
| Separate restore | Another machine with no source branch/clone; original branch moved or checked out elsewhere; destination collision; duplicate request; per-repo different HEADs; process death mid-restore. Existing branch/path/conversation untouched, recovery refs retained. |
| Retention/delete | Source worktree/project hard-deleted, machine removed, old recovery, inbox dismissed, successful restore: refs/catalog remain. Explicit exact delete only; moved ref refused; permissions/ruleset denial; partial delete/resume; audit/no erasure claim. |
| Host capability | Actual GitHub `refs/heads/reliant-recovery/...` create/read/full fetch/restore/delete with supported credential class; rulesets, workflow permissions and limits fail safe. Bare remote alone does not prove GitHub support. |

Test commands from current source, run in `reliant/`:

- Inner loop: `task test:short -- ./internal/worktreereclaim/... ./internal/worktreesweep/...` and each touched package's scoped tier; `task test:affected:plan` / `task test:affected` for reverse dependencies and focused frontend tests.
- DB tests: use current pgtest-backed `internal/db` test harness; no real-data database. If selecting a maintenance server, follow `FORGE_TEST_POSTGRES_URL` major-version contract in current `testutil.go`.
- Before handoff: scoped full `task test -- ./internal/worktreereclaim/... ./internal/worktreesweep/... ./internal/toolexec/daemonruntime/... ./internal/handlers/worktree/... ./internal/handlers/inbox/... ./internal/db/... ./internal/store/...`; changed tests repeated three times plus targeted race/crash proofs. Gate inherently slow subprocess/network fixtures under `testing.Short()` without gutting assertions; make ordinary fixtures fast.
- Generation owner: pinned generation/check and forge lint. Integrator: `go build ./...`, `task ci:merge`, then required `task ci` / full lane per current Taskfile/local policy. Long commands run in background with `shell_wait`. Never `make test` / `make test-all` or stop another agent's stack.
- No CI-budget wait: exact local results are the merge evidence; do not waive new failures or call skipped safety tests passed.

## 8. Rollout, fix-forward and boundaries

1. Add schema/contracts and read-only eligibility/catalog observability. Newly observed workspaces begin a fresh seven-day evidence interval; do not backfill “inactive” from directory mtimes. Disable old local-only destructive sweep/confirmed cleanup during mixed-version rollout, and require new daemon capabilities. Old daemons hold, never get fallback delete instructions.
2. Enable capture/publication/verification without deletion; run actual disposable GitHub proof and crash/race harness. Surface held reasons and branch visibility/retention semantics. Count would-remove results; do not manufacture urgency or loosen holds to increase reclaim rate.
3. Enable the approved automatic seven-day **eligible** Git-only finalizer only after the safety matrix passes. Respect keep-files; automatic operation is not a blanket backup promise for all workspaces. Provide inbox restore/deletion before enabling removal.
4. Fix-forward for failures: disable deletion admission first; retain refs, receipts, local pins, original/quarantined content and catalog; reconcile/resume with a corrected version. Never mass-prune remote branches, reset live branches, drop recovery schema, or “roll back” by re-enabling local-only deletion. Schema is forward-only; no down migration. Do not restore an older unsafe cleanup binary against the new lifecycle without disabling its destructive loops.

No architectural policy question remains for implementing this approved narrow design. Provider/OS/format/scanner capabilities are **testable eligibility gates**, not invitations to add a second storage system or silently broaden scope. The implementation must expose limitations honestly: Git-host administrator deletion, arbitrary undiscoverable secrets and hostile unmanaged writers are not guarantees a branch namespace, detector or Git worktree lock can magically provide.
