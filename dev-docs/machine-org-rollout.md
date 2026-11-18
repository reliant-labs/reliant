# Machine orgs: rollout, post-deploy check, recovery

reliant#2861 keeps each machine's project checkouts inside the machine's
organization. This page covers what changes when it ships, how to check it
worked, and how to undo the one destructive step (the connect-time cleanup)
without touching any file.

## What ships, in order

1. **Release 1 (reliant#2861):**
   - `daemons.org_id`: the machine's org, mirrored from control-plane on
     connect.
   - Org-aware project readers everywhere: create, attach, clone, the
     connect reconcile, by-path resolution and the dir heal.
   - New per-org unique indexes. The old per-user keys `projects_user_id_path_key`
     and `projects_user_remote_url_uniq` **stay**.
   - The cleanup, its audit table `project_daemon_org_cleanups`, and its
     kill switch.
2. **control-plane#2897** (deploys independently):
   - Introspect reports `daemon_org_id` for a daemon-bound token. A failed
     read refuses the introspection rather than report "no org".
   - The DAEMON_STATE snapshot carries each machine's `orgId`, or
     `orgUnresolved` when the read failed.
3. **Release 2 (the key-swap PR):** drops the per-user keys, so the same path
   or repo can be a project in two orgs.
   - **Ship it only once a release containing release 1 is the rollback
     target.** Every binary that can then run against the database must
     resolve projects by org. The release before release 1 resolves by
     (user, path) alone and would pick an arbitrary org's project.

Until control-plane#2897 is deployed, every machine's org is unknown. A
machine of unknown org holds only its owner's **default** org's projects, and
nothing is cleaned up. The cleanup only ever runs for a machine whose org
control-plane named.

## The cleanup and its kill switch

On each connect, for a machine of a known org, the reconcile detaches the
checkouts of projects in another org from the machine.

- **When:** the cleanup re-reads the project's org under the project row's
  lock, the same lock an org move takes. It detaches only if the project is
  still in another org, so a project moved into the machine's org meanwhile
  keeps its checkout. A project with no org yet is never detached.
- **What is detached:** what a user's detach removes, through the same code
  (`db.Repo.DetachProjectDaemonOfOtherOrg` and `DetachProjectFromDaemon` share
  their locks and their removal): the checkout record (`project_daemons`), the
  machine's synced config of the project (its `project_machine_catalogs` row:
  workflows, presets, skills, memories, config), and that config's share of
  the project's config union, which is rewritten from the machines left. No
  project is deleted, no file is touched, and no user detachment is recorded,
  so a project moved back into the machine's org is found on it again.
- **Audit:** both rows are written whole to `project_daemon_org_cleanups`
  (`detached_row`, and `detached_catalog`, NULL when the machine had synced no
  config of the project), in the same transaction as their delete.
- **Logs:** each removal logs `machine-org cleanup detached another
  organization's project` with `projectID`, `projectOrgID`, `machineOrgID`
  and `path`.
- **Kill switch:** `RELIANT_MACHINE_ORG_CLEANUP=off` on the gateway stops the
  cleanup. Nothing else changes: new records still keep to the machine's org.
  The gateway reads it at start, so a restart applies it. In KCL it is
  `machine_org.cross_org_cleanup`.

## Post-deploy check

Run these against the reliant database of the environment (`reliant_cp` in
prod) with `reliant forge db query <env> … --confirm-env <env> --reason "<why>"`.

1. **Machine orgs are being recorded.** Run this an hour after release 1 and
   control-plane#2897 are both deployed:

   ```sql
   SELECT count(*) FILTER (WHERE org_id IS NOT NULL) AS known,
          count(*) FILTER (WHERE org_id IS NULL)     AS unknown
   FROM daemons;
   ```

   `known` should grow as machines reconnect. Look for `machine has no known
   organization` in the gateway logs for the rest.

2. **Recorded orgs match control-plane.** Export `daemons (id, org_id)` from
   reliant, then compare it with control-plane's `controlplane.daemons (id,
   org_id)` (control-plane database, `control_plane_cp`). Every non-NULL
   reliant `org_id` must equal control-plane's. A mismatch means an
   introspection named the wrong org. Turn the kill switch off and
   investigate before the next connect detaches more.

3. **What the cleanup removed:**

   ```sql
   SELECT date_trunc('hour', detached_at) AS hour, machine_org_id, count(*)
   FROM project_daemon_org_cleanups
   WHERE restored_at IS NULL
   GROUP BY 1, 2 ORDER BY 1;
   ```

   Expect one burst per machine on its first connect after deploy, then
   nothing. Any of these needs a look:
   - the count keeps growing;
   - a row whose `project_org_id` equals the machine's org (the cleanup should
     never remove those);
   - a user reporting a project missing from a machine.

## Recovery: restore detached records

This puts the records back exactly as they were. No file is touched; the
folders were never removed.

1. Stop the cleanup first: set `RELIANT_MACHINE_ORG_CLEANUP=off` on the
   gateway and roll it. Otherwise the next connect detaches the rows again.
2. Restore. Narrow the `WHERE` to the machines, users or time window you need:

   <!-- machine-org-restore-sql: TestMachineOrgCleanup_DocumentedRestoreBringsTheCheckoutBack runs this block -->
   ```sql
   BEGIN;
   WITH todo AS (
       SELECT id, detached_row, detached_catalog FROM project_daemon_org_cleanups
       WHERE restored_at IS NULL
         AND detached_at >= '<from>'          -- narrow as needed
       FOR UPDATE
   ), put AS (
       INSERT INTO project_daemons
       SELECT (jsonb_populate_record(NULL::project_daemons, todo.detached_row)).*
       FROM todo
       ON CONFLICT (project_id, daemon_id) DO NOTHING
       RETURNING project_id, daemon_id
   ), put_config AS (
       INSERT INTO project_machine_catalogs
       SELECT (jsonb_populate_record(NULL::project_machine_catalogs, todo.detached_catalog)).*
       FROM todo
       WHERE todo.detached_catalog IS NOT NULL
       ON CONFLICT (project_id, daemon_id) DO NOTHING
       RETURNING project_id, daemon_id
   )
   UPDATE project_daemon_org_cleanups c SET restored_at = now()
   FROM todo WHERE c.id = todo.id;
   COMMIT;
   ```

   `ON CONFLICT DO NOTHING` keeps a record, or a synced config, the machine
   has re-created since the cleanup. The row of a project or machine deleted
   since then fails its foreign key. Drop it from the narrowed set and re-run.
3. The restored checkout and the machine's synced config are back as soon as
   step 2 commits: the machine's own runs, and workflow provenance, read them
   directly. The project's config **union** (`project_configs`: the workflows,
   presets and skills every run of the project sees) is derived in Go, not
   SQL. It is rewritten from every machine's synced config whenever a machine
   pushes the project's config, and a machine pushes it on connect for each
   checkout it holds. A restore is for a cleanup that misjudged, such as a
   machine whose org control-plane named wrongly: fix that cause, then roll
   the gateway so the machine reconnects. Its restored checkout is then a
   project of its org, it pushes the project's config, and the union has its
   files again. (A checkout restored onto a machine of another org stays
   outside what that machine resolves, so nothing re-pushes it.)
4. Leave the kill switch off until the cause is fixed. Turning it back on
   cleans up again on the next connect, and records it again.
