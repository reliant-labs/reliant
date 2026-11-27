# Command memory-controller rollout

## Post-deploy check

On a canary Linux workspace, restart the daemon onto the new revision. Resolve
its delegated parent from `/proc/<daemon-pid>/cgroup` (the daemon is in `self/`).
Check that `memory` appears in both the delegated parent's and `commands/`'s
`cgroup.subtree_control`, and that a newly started `commands/cmd-*` has
`memory.high`. Run a slotted command and verify that its bound is lowered while
it runs and restored when its last wrapper exits. Verify an interactive sibling
still responds under contention.

Record `nr_descendants` and `nr_dying_descendants` from the delegated parent's
and `commands/cgroup.stat` before and after repeated short commands, then again
after the commands have exited and a quiet interval. Also record `memory.current`
and `memory.events`. Do not broaden rollout if dying descendants grow across
successive batches without draining, memory grows without settling, or interactive
work regresses. A successful restart alone is not evidence of safe accumulation.

## Rollback

`RELIANT_HEAVY_MEMORY_HIGH=off` on wrapper invocations disables the soft bound
only. It does **not** disable the memory controller, stop command memcg creation,
or undo dying-memcg accumulation.

For a controller rollback, restore the daemon built from the pre-PR merge-base
revision `51ba087ab707dbb8b1100bdaf2879b7a104a444f` (or the recorded pre-rollout
artifact of that revision), which does not enable memory for command cgroups.
Drain work and preserve conversation state before replacing the workspace; do
not kill a shared active workspace to perform this check. Recreate the workspace
container/delegated cgroup hierarchy and start that daemon in the fresh hierarchy.
A daemon restart inside the existing hierarchy is insufficient: previously enabled
`cgroup.subtree_control` settings survive it. The fresh hierarchy must retain its
normal pids delegation but not enable memory in the delegated parent's or
`commands/`'s subtree control. If the entrypoint/operator enables memory there,
restore its pre-rollout configuration as well before starting the daemon.

Verify that a new command has no `memory.high` interface and that the two subtree
control files do not contain `memory`. Repeat the short-command batches and check
`cgroup.stat` for no new command memcg accumulation. Existing dying memcgs may
outlive directory removal while kernel references drain; if that persists, recycle
the drained workspace host rather than claiming that disabling memory.high fixed
it. Keep rollout stopped until the fresh-hierarchy checks succeed.
