# Frontend migration: bounded follow-up checklist

Latest bounded read slice verified against fresh main `2a024bf71` on 2026-11-09.
Audit/devlog #2448, demo mocks #2454 and imperative PR lookup #2480 are merged.
Read this together with `forge-frontend-migration.md`; neither checklist claims
complete frontend/backend equivalence. No competing filesystem read PR was found.

## Verified current/adopted

- [x] #2363 settings/provider imperative mutations are merged; no further pin
  or duplicate adoption PR needed.
- [x] Runtime 0.13.8 is already locked/published. Its mock-transport entry exports
  `serviceDefaults`, `mockService`, `mockValue`, types, read-only guard,
  passthrough-first and deadline/fallback handling. No package/pin change here.
- [x] Demo imports those published helpers in `demoTransport.ts`,
  `consoleScenario.ts`, `demoScope.ts`, `demoServiceDefaults.ts` and the descriptor
  regression tests. Removed `demoData/forgeMockCompat.ts` and
  `demoData/forgeMockValues/{mock-values,mock-rules}.ts`.
- [x] Keep product behavior in `demoData`: service allowlist, scenario story,
  scope filters, request/response completion at the composed-handler boundary,
  fixture/model mapping and read-only UI. The full app-generated registry also
  carries Reliant services, so it must not replace this demo's service selection.
- [x] Adoption acceptance: fixture reads use no gateway; remote reads use live
  answers, fall back on failure/deadline (including a backend that ignores the
  deadline), and update the fallback banner. Caller cancellation stays canceled,
  not fixture success. Writes never reach handlers/backends. Unknown fixture RPCs
  stay Unimplemented. Pattern/oneof/echo descriptor regressions remain covered.
- [ ] CP console devlog wiring is a separate independent control-plane PR,
  branch `fix/console-forge-dev-browser-logs`; see its
  `docs/forge-frontend-migration.md`. No merge/pin dependency on this demo slice.

## Validation for this slice

From `frontends/web`, explicitly run:

```sh
pnpm exec vitest run \
  src/components/Forge/Demo/demoData/__tests__/demoData.test.ts \
  src/components/Forge/Demo/demoData/__tests__/forgeMockValues.test.ts \
  src/components/Forge/Demo/demoData/__tests__/consoleScenario.test.ts \
  src/components/Forge/Demo/demoData/__tests__/busyApp.test.ts \
  src/components/Forge/Demo/__tests__/DemoReadOnlyControls.test.tsx \
  src/lib/__tests__/connect.demo.test.ts
pnpm exec tsc -b
```

Validation: **6 scoped files / 56 tests passed**, `tsc -b` and changed-file
eslint passed. No unscoped Vitest, release/gate/deploy or production proof is
implied. Broader current config/auth/transport/ownership findings
are in #2448; they are not all reimplemented here.

## Concrete next unary slices (not implemented)

Current generated targets exist in `worktree-service-hooks_gen.ts` and
`file-system-service-hooks_gen.ts`. Each write currently has service-wide generated
invalidation. Do not silently replace the product's narrower per-worktree refresh
without testing that change. Keep public adapter return types and nested `repoId`.

### Next small PR: `src/api/git.ts`, one adapter method per test row

| Adapter -> RPC/helper | Preserve besides generated call | Acceptance |
|---|---|---|
| `stageFiles` -> `worktreeServiceMutations(qc).stageFiles` | files, repoId, mapped message/files; `afterWrite(id)` notification | one RPC, nested repo forwarded, worktree reads refreshed, git-status notification once; failure does not notify |
| `unstageFiles` -> `.unstageFiles` | same as stage | same test matrix, unstage request |
| `revertFiles` -> `.revertFiles` | files/repoId and afterWrite | failure preserves dirty UI; refresh once on success |
| `commitChanges` -> `.commitWorktree` | message/repoId, mapped message/output, afterWrite | no write retry; error preserved; refresh once |
| `pushChanges` -> `.pushWorktree` | `afterWrite(id, false)` (no legacy git-status event) | cached PR/status refresh but no extra notify; mapped output preserved |
| `pullChanges` -> `.pullWorktree` | `afterWrite(id)` | legacy notify preserved, failure no notify |
| `createPullRequest` -> `.createWorktreePR` | body default, repoId, prUrl -> pr_url; notify=false | no accidental second PR, return mapping and PR cache refresh |
| **Implemented in this slice:** `getExistingPR` -> `worktreeServiceQueries.getWorktreePR(req)` via fetchQuery | empty wire fields -> undefined, repoId; require a fresh remote PR answer | concurrent read shares generated key, staleTime=0/retry=false/gcTime=0; no negative-cache surprise |

Follow-up at main `0874606bb`: the first independent unary adoption is only
`getExistingPR`. It uses the already-pinned generated query definition, always
revalidates and does not retry or retain unused remote branch metadata. UI
`useWorktreePR` still adds its observed-branch key segment; that behavior is not
changed or falsely claimed to share the exact imperative cache entry. Public v1
adapter signature/result mapping is unchanged. Transport tests pin concurrent
sharing, worktree/repo isolation, fresh negative answers and stale-success errors.
No mutation/invalidation scope is changed in this slice. Verification: 5 scoped
files / 24 tests, `tsc -b`, and changed-file eslint pass. `prove_test` confirms
concurrent identical calls made two RPCs before the change and one afterward.

Next consider stage/unstage/revert, not all seven writes at once: generated
mutation helpers invalidate the whole service, whereas `afterWrite` is narrower.
That observable broadening needs explicit tests before swapping call sites.
Run scoped
`RecentChanges.{flow,race,machineWake}.test.tsx` and new transport/cache tests;
`tsc -b`. `afterWrite` also notifies `gitStatusStore`; do not delete it wholesale.

### Filesystem READ slice (implemented here); writes remain separate

`getFileTree`, `getFileContent`, `getFilePreviewInfo` and `getFilePreviewBlob` now
use the already-pinned generated query definitions. An unobserved transient
QueryClient replaces delimiter-joined in-flight maps, removing each exact query
synchronously after settlement; older waiters cannot evict a newer same-key read.
This is deliberately not the app's observed cache: focus/invalidation cannot
refetch an editor buffer, and no file/preview bytes persist after completion.
Options are staleTime=0, gcTime=0, retry=false, networkMode=always (browser offline
must not suppress an explicit local-daemon read). Generated queryFn forwards its
AbortSignal; canceled RPC rejection remains a rejection, never cached fallback
success. Public signatures, path normalization, chat/project/worktree/depth scope,
hidden filtering, sorting, boundary children and wire/view/blob mapping stay.
No write, git notification, error toast or optimistic policy changes.
Validation: **6 scoped files / 29 tests**, `tsc -b` and changed-file eslint pass.
`prove_test` confirms generated request keys fix the former delimiter-key
collision between different chat/path scopes. Tests also preserve canceled/error
rejection, no retries, offline explicit reads and app-cache cancellation isolation.

Stage/unstage/revert dependency: forge#1989 merged but **newest published runtime
0.13.9 still exposes only signal in ServiceMutationCallOptions**, not invalidate.
Existing manifest is ^0.13.8 and no pin is changed here. Wait for actual published
seam + coordinated pin; no local package patch or release cut. Commit/push/pull/
create-PR and filesystem writes stay separate later slices.

CP#2285 current repaired head 913f0eb2d has review:passed/merge:queue alongside a
stale merge:failed label. Trusted numeric-ID evidence only reports failure on old
abffb4cc7, not the repaired head; no active fix lease was found. Do not invent a
new blocker or duplicate the steward. Live smoke guarantees remain in its PR.

| Adapter -> generated target | Required preservation / test |
|---|---|
| [x] `getFileTree` -> `.getFileTree` query | project/worktree/chat-machine/path/depth identity, showHidden=true wire request, frontend hidden filtering and sorting; empty project returns []; replace pending-map only with fresh, non-persistent cache semantics |
| [x] `getFileContent` -> `.getFileContent` query | normalize absolute/relative path before key; chat-machine isolation; concurrent dedup only, no stale editor content |
| [x] `getFilePreviewInfo` -> `.getFilePreviewInfo` query | missing info rejects, wire/view enum mapping, normalized path and chat machine |
| [x] `getFilePreviewBlob` -> `.getFilePreview` query | binary bytes and MIME preserved; bound/avoid persistent large blob caching |
| `saveFileContent` -> mutations `.saveFileContent` | no-project no-op, normalized path, content, worktree/chat; git-status notification after success, never failure |
| `createFile` -> `.createFileOrFolder` | FILE enum/content, require project, chat machine, one success notification |
| `createFolder` -> `.createFileOrFolder` | DIRECTORY enum/empty content, same context and notification |
| `deleteFileOrFolder` -> `.deleteFileOrFolder` | project/chat/worktree and one post-success notification |
| `copyFile` / internal `copyOnServer` -> `.copyFile` | source/destination/chat context; copyFile notifies once |
| `copyDirectoryRecursive` -> existing recursion over adapters | immediate-child depth, hidden-file behavior, failure propagation; do not replace with a guessed RPC |
| `moveFile` -> copy then delete through adapters | delete only after copy succeeds, preserve partial-failure behavior and existing notification count |
| `replaceInFiles` -> mutations `.replaceInFiles` | explicit QueryClient parameter, result mapping, `fileRewrites` dirty-buffer policy, search invalidation and git-status refresh; not merely a mutation-factory swap |

Read acceptance: existing `src/api/__tests__/fileSystem.{inflight,chatMachine}.test.ts`
and file viewer/tree tests. Write acceptance: add request/result/failure transport
rows and assert editor/search/git effects; never introduce mutation retries.

## Filesystem writes: contract hardening, NOT helper migration complete

Fresh independent slice at main `3773fd8bc`; no competing write PR. Existing
pinned generated client is retained with explicitly app-owned refreshes: the
published runtime still lacks the `invalidate` override, so adopting its broad
service invalidation would change editor/search/cache behavior.

- [x] Actual bug fixed: `moveFile` now captures project/chat once for copy AND
  delete. Previously a UI switch while copy was pending made the public delete
  adapter resolve another chat/project and potentially delete on another disk.
  Copy must finish before delete; failed/canceled copy never deletes, failed
  delete rejects without retries or success refresh; full success refreshes once.
- [x] New `fileSystem.writes.test.ts` pins save/create-file/create-folder/delete/
  copy/move/replace wire paths, project/chat/worktree, save no-project no-op,
  no retries/failure refresh, replace result mapping, successful-only editor
  rewrite notifications and exact search-only invalidation. Other content
  entries stay untouched. 5 scoped files / 36 tests, typecheck and lint pass;
  `prove_test` confirms move-scope regression fails before and passes after.
- [ ] Generated mutation-helper adoption remains outstanding for ALL filesystem
  writes. This contract/fix slice does not cosmetically relabel adapters as
  migrated. Recursive-copy routing/lifecycle is separately product-owned.

### Forthcoming runtime package dependency (read-only metadata proof)

Newest published npm package is **0.13.9**, gitHead `56e8024f38c5` (before #1989).
The release planner's compute-only `--plan` mode (no worktree/build/tag/publish)
reports **0.13.10 / web-runtime/v0.13.10** for merged #1989 commit `7944403df090`
and current forge main `3afc116e704b`: patch over 0.13.9, state `planned`.
That is the forthcoming package containing the seam **if released from these
commits**, not evidence that it is on npm yet. Recheck actual tarball declarations
and coordinate consumer pin after publication. No release cut, pin PR or local
package patch was performed.

## Owner decisions left open

- Streaming wrappers now delegate to Reliant SDK `UserStream`, `ChatStream` and
  `ProcessOutputStream`; sequence/replay/resume/reducer semantics are not generic
  unary hooks. No framework extraction without owner API approval.
- Keep logger/Electron IPC/diagnostic policies separate from the dev sink. A
  generic forge serializer/logger lifecycle is an architectural proposal, not
  part of this cleanup.
- Keep error-shell consent, redacted support reports, stale-build recovery and
  custom product UI. RuntimeShell/ErrorBoundary substitution requires owner-led
  acceptance criteria; do not replace product screens with starter pages.
