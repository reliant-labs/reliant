# Recovering the read-only merge queue and its SLO report

`CI result` is still the required merge-group check. The candidate workflow
runs with `contents: read`, no persisted checkout authentication, and no
GitHub token in its gate shells. The separate `Merge queue SLO` workflow
reports timing from the default branch. It is never a required check.

## Verify and disable the issue writer

After merge, inspect the first over-budget `Merge queue` run and confirm it
uploads `merge-queue-slo-<attempt>` and that the matching `Merge queue SLO`
run either updates the rolling issue or leaves it quiet under the one-hour
comment limit. An ordinary run under budget should not upload that artifact.

If publication is noisy or evidence validation fails, the owner can open
**Actions → Merge queue SLO → … → Disable workflow**. The command equivalent
for someone with workflow administration is
`gh workflow disable merge-queue-slo.yml --repo reliant-labs/reliant`.
Do not disable `Merge queue` or change branch protection. The required
check and its timing summary keep working; only rolling issue publication
stops. Fix the listener on the default branch, test it with the fixture
archive, then re-enable it via the same menu or
`gh workflow enable merge-queue-slo.yml --repo reliant-labs/reliant`.

## Verify the gate and recover from public API faults

On the first normal merge group, confirm the `golangci (changed) passed`
step reads **its own run attempt** and permits a success. A non-success,
ambiguous state, or unavailable verdict must not pass. For an approved
incompatibility case, check the gate logs attribute approval to the owner
on that PR, not just the presence of a label. An unreadable later page must
leave the incompatibility blocked.

If the public API returns a transient 403, 429, or 5xx, let the normal
merge-queue retry path rerun the unchanged entry after GitHub recovers; do
not override the required check or use a manual merge. The lint verdict
uses at most eight anonymous requests, with its final request reserved for
the original deadline. If anonymous Actions reads are persistently failing,
open a forward-fix PR replacing the poll with native job dependencies and
aggregating both outcomes under the same `CI result` check name. Keep the
candidate jobs read-only and the issue writer separate. For a persistent
compatibility API fault, defer approved-incompatibility changes until the
owner-approved evidence can be read again; a forward fix can move just the
approval check to a trusted read-only step, but may not infer approval from
missing data. Never revert by returning `issues: write` or a persistent
checkout token to the candidate workflow.
