# Frontend migration: bounded follow-up checklist

Verified against fresh main `f2759e070` on 2026-11-08. This branch is independent
of the broader audit/devlog PR [#2448](https://github.com/reliant-labs/reliant/pull/2448),
which was still open when ownership was checked. Do not duplicate its devlog
adoption. This file supplements that PR's `forge-frontend-migration.md` without
creating the same file independently and forcing a merge conflict. It records
new completion rows and concrete next methods, not complete frontend/backend
equivalence. Read both checklists once #2448 lands.

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
| `getExistingPR` -> `worktreeServiceQueries.getWorktreePR(req)` via fetchQuery | empty wire fields -> undefined, repoId; require a fresh remote PR answer | concurrent read shares generated key, staleTime=0/retry=false; no negative-cache surprise |

Start with stage/unstage/revert, not all eight at once. Run scoped
`RecentChanges.{flow,race,machineWake}.test.tsx` and new transport/cache tests;
`tsc -b`. `afterWrite` also notifies `gitStatusStore`; do not delete it wholesale.

### Subsequent PRs: `src/api/fileSystem.ts`

| Adapter -> generated target | Required preservation / test |
|---|---|
| `getFileTree` -> `.getFileTree` query | project/worktree/chat-machine/path/depth identity, showHidden=true wire request, frontend hidden filtering and sorting; empty project returns []; replace pending-map only with fresh, non-persistent cache semantics |
| `getFileContent` -> `.getFileContent` query | normalize absolute/relative path before key; chat-machine isolation; concurrent dedup only, no stale editor content |
| `getFilePreviewInfo` -> `.getFilePreviewInfo` query | missing info rejects, wire/view enum mapping, normalized path and chat machine |
| `getFilePreviewBlob` -> `.getFilePreview` query | binary bytes and MIME preserved; bound/avoid persistent large blob caching |
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
