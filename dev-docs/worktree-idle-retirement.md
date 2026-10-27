# Whole idle-worktree retirement

The configuration key `worktree_live_shed_idle` / environment variable
`RELIANT_WORKTREE_LIVE_SHED_IDLE` now controls **whole recoverable-worktree
retirement**, not live generated-file shedding. Its default is 24h; 0 disables
it. `RELIANT_WORKTREE_AUTO_ARCHIVE=on` is additionally required for archival;
dry-run/off never removes live worktrees.

A probe does not delete anything. It reports eligibility only when every
checkout is clean, its commits are pushed or merged, and its complete contents
have been quiet for the configured floor. This includes writes inside
node_modules, pinned tools, build outputs and caches. A pending, active, or
paused/resumable execution bound through either its chat or a spawned thread
excludes the worktree. Failed activity/process/layout lookups fail closed.

Pushed commits are **not** permission to lose unique files. Dirty/untracked
work, protected descendants (.env, databases, data, secrets), nested or unknown
repositories, special files, mounts, unreadable/changed paths, and unsupported
platform inspection retain the whole workspace. No recursive ignored-subtree
`RemoveAll` runs against live or held workspaces.

The server rechecks execution and last-activity under the worktree row lock,
then records `archived_because: recoverable-idle`. Actual deletion uses the
existing archive fence and the bounded whole-worktree retirement protocol:
exclusive daemon runtime admission, fresh cwd/executable/fd/mapping checks,
quarantine and complete revalidation, Linux descriptor-relative no-follow,
no-mount traversal/deletion. Quarantine retained on uncertainty is not counted
as reclaimed disk. This is conservative managed-runtime exclusion and change
detection, not an arbitrary external writer freeze.

A conversation using an automatically retired workspace can continue: the
row is revived only for the proven branch-recoverable lifecycle and without
saved recovery catalog/legacy snapshots. Every tool batch sends the pending
archive fence until acknowledged. Failed/deferred retirement blocks machine
tools safely instead of racing an old remover or silently switching branches.
Dirty/untracked saved recovery state requires the explicit recovery UI.

Partial disposal remains held in quarantine and is never automatically revived
as a complete checkout. Manual confirmed recovery/removal remains a separate
existing path. Linux is initially supported for automatic bounded deletion;
unsupported platforms retain files.
