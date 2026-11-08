# Projects on several machines: post-deploy check and rollback

The web app now browses, makes folders and creates projects on a machine the
user names (`DirectoryPicker`'s machine selector, "Use a folder on
<machine>", Settings → Projects → machines), and the API server refuses to
remove or reclone a machine's checkout while branch workspaces of it remain
(live, or archived with files or a branch still to clean up). Both halves are
additive: `ListDirectoryResponse.daemon_id` and `CreateDirectoryResponse.daemon_id`
are new optional fields, and no migration ships.

## During the rollout: a web app ahead of its API server

The web app and the API server do not ship at the same instant, and a user's
Electron app may carry an older web bundle. An API server older than
`ListDirectoryRequest.daemon_id` ignores the field, lists the DEFAULT machine
and echoes no `daemon_id`. The new web app treats a missing echo on a request
that named a machine as "not that machine's listing": it hides the entries,
disables "New folder" and "Use this folder", and says the server cannot browse
that machine yet (`DirectoryPicker.oldServer.test.tsx`). Likewise
`createProjectWithResult` refuses a `CreateProject` answer whose checkout is
not on the requested machine. So the web app can ship first and fails closed;
with no machine named it behaves as before.

## Post-deploy check (first environment that ships both halves, then prod)

Use an account with two connected machines, A (the default) and B, each with
an empty scratch folder in its home directory.

1. **Listing is the named machine's.** Add project → choose a folder → switch
   the picker to B. Expect B's home folder (compare with `ls ~` on B), not A's.
   Switch back to A: A's folders, and no flash of B's.
2. **New folder lands on B.** On B, "New folder" `rollout-check`. Expect it on
   B's disk and absent from A's.
3. **Project created on B.** "Use this folder" on `~/rollout-check` on B.
   Expect a new project whose Settings → Projects → machines lists B at that
   path, and a new chat on it whose `pwd` tool call answers from B.
4. **Same folder, existing project.** Repeat 3 for the same path on B. Expect
   the toast "Added B to existing project …", not a second project.
5. **Attach on A.** In Settings → Projects → machines, "Use a folder on A" for
   a checkout of the same repository. Expect A listed beside B.
6. **Remove is guarded.** Create a branch workspace of the project on B, then
   "Remove from machine (delete files)…" on B. Expect the refusal "1 workspace of … live on B;
   archive them before removing …" and the folder still on B's disk. Archive
   the workspace; once its clean-up has run, the remove succeeds and the
   folder is gone from B. Repeat with "Reclone…".
7. **Older-server refusal** (only where a web build can be pointed at an older
   API server, e.g. dev): with the new web app against the previous API
   release, step 1 on B shows "This Reliant server cannot browse B yet", with
   no entries and "New folder" / "Use this folder" disabled.

Clean up: delete the `rollout-check` project and the folders on A and B.

## Rollback

- **A wrong listing, folder or project on the wrong machine (steps 1-5).**
  This is the web app or the API server's `daemon_id` routing
  (`internal/handlers/file_system/fs_proxy.go`). Revert the PR's commit on
  `main` and redeploy the web app; the old web app never sends `daemon_id`, so
  every call goes back to the default machine. The API server's new response
  fields are ignored by the old web app and need no revert. Projects or
  folders created on the wrong machine are removed by hand (Settings →
  Projects, or the machine's disk).
- **The remove/reclone guard misbehaves (step 6).** A refusal with no
  workspace left is a server defect: fix forward in
  `requireNoWorkspacesOn` (`internal/handlers/project/rpc_detach_project_from_daemon.go`),
  or revert the server change (the remove and reclone then run unguarded, as
  before this PR). A remove or reclone that succeeds while a workspace of
  that checkout remains (or is created while it runs: the guard and the
  daemon command hold the checkout's exclusive lock,
  `Repo.TearDownProjectCheckout`, which a workspace insert shares) is
  also a server defect: fix forward there, and rebuild the workspace from its
  branch if the branch was pushed.
- **A workspace create is refused with "has not reported whether it
  finished".** A remove or reclone of that checkout timed out (or the server
  died while waiting), so `project_daemons.teardown_pending_at` stays set
  while the machine may still be deleting. Expected: run the remove or
  reclone again with the machine online, which clears it. A mark left on a
  checkout whose remove or reclone did answer is a server defect
  (`teardownSettled`, `internal/handlers/project/rpc_project_daemons.go`):
  fix forward; as a stopgap clear that one row's `teardown_pending_at`.
- **One additive migration** ships (`project_daemons.teardown_pending_at`,
  nullable, unread by the previous release), so either half still rolls back
  alone. During a rolling deploy a previous-release server neither sets nor
  reads the mark.
