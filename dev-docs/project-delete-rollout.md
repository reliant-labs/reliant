# Project delete with a restore window (reliant#2890): post-deploy check and rollback

reliant#2890 makes `DeleteProject` schedule the delete instead of deleting
(control-plane `docs/design/unified-shell-and-projects.md` §11.3, D18):

| Change | Where |
|--------|-------|
| `projects.delete_after`, `deleted_by`, `purge_started_at`, and the partial index `idx_projects_delete_after` | `db/migrations/20261118040600_project_delete_after.up.sql`, `…040626_projects_delete_after_index.up.sql` |
| The `project_delete_admission` triggers: a new chat, worktree, run or live trigger in a deleted project is refused under a lock the delete serializes with | `db/migrations/20261118103211_project_delete_write_admission.up.sql` |
| Delete pauses the project's enabled triggers (`paused_reason = 'project_deleted'`) and their Temporal schedules; restore resumes exactly those | `internal/store/project_schedule.go`, `internal/handlers/project/project_delete.go` |
| The worker's `project_purge` loop purges projects whose window has ended, every 15 minutes | `internal/app/providers_worker.go` (`ProjectPurge`) |

The tests prove each piece against a real Postgres. Whether the worker runs
the loop, the triggers exist, and Temporal follows the pause and resume can
only be seen in a deployed environment, so check after the release that
carries this change is live. Every command below is read-only except the
smoke test, which deletes and restores a throwaway project you create for it.
None takes a credential in argv: `reliant` uses your stored login
(`reliant auth login`), and `reliant forge db query` uses the org's
`db:query` grant.

## After deploy: the checks

Run them in order once the release is live in prod.

1. **The release is healthy.** `reliant forge obs check prod --wait --json`,
   branching on its exit code as usual (0 good; 1 regressed: read its
   `suggested[]`; 2 not proven good; 8 check again later).

2. **The purge loop runs on the worker.** Each worker replica logs
   `project purge: running` once at start:

   ```bash
   reliant forge obs logs prod "project purge" --service reliant-temporal-worker --since 2h
   ```

   Expect one `project purge: running` line per worker replica started since
   the deploy. Later lines (`project purge: pass done`, `purged a project
   whose restore window ended`) appear only once a project's 7 days have
   passed; any `could not purge` or `could not list the projects due` warning
   is a failure (below).

3. **The admission triggers and the columns exist.**

   ```bash
   reliant forge db query prod --database reliant --confirm-env prod \
     --reason "post-deploy check: reliant#2890 admission triggers" \
     "SELECT tgrelid::regclass AS on_table, tgname, tgenabled FROM pg_trigger
       WHERE tgname LIKE 'project_delete_admission%' ORDER BY 1, 2"
   ```

   Expect six rows, every `tgenabled` = `O`: `chats` (`project_delete_admission`,
   `project_delete_admission_move`), `triggers` (`project_delete_admission`,
   `project_delete_admission_update`), `workflows` and `worktrees`
   (`project_delete_admission`). Then the index is valid:

   ```bash
   reliant forge db query prod --database reliant --confirm-env prod \
     --reason "post-deploy check: reliant#2890 delete_after index" \
     "SELECT indisvalid FROM pg_index WHERE indexrelid = 'public.idx_projects_delete_after'::regclass"
   ```

4. **Smoke test: delete, refuse, restore, on a throwaway project.** Signed in
   to prod as yourself, in an empty directory on a machine of yours:

   ```bash
   mkdir -p /tmp/rl-delete-smoke && cd /tmp/rl-delete-smoke
   reliant project create --name rl-delete-smoke --path .
   reliant trigger create --project . --name smoke --interval 1h \
     --message "smoke" --daemon <your machine's hostname>
   reliant trigger list --json            # note the smoke trigger: enabled, next_fire_at set
   reliant project delete rl-delete-smoke --yes
   ```

   - The delete prints `It can be restored until <date, 7 days out>`.
   - `reliant project list` no longer shows it; `reliant project restore`
     lists it.
   - A new chat in it is refused: `reliant workflow run --project-id <its id> --message hi`
     fails NotFound, and so does `reliant trigger create --project-id <its id> …`.
   - **The schedule paused (check 5).**

   Then `reliant project restore rl-delete-smoke` prints `1 automation(s) the
   delete paused are on again`, the project lists again, and the trigger is
   enabled. Finish with `reliant project delete rl-delete-smoke --now --yes`
   (it is purged at once) and `rm -r /tmp/rl-delete-smoke`.

5. **Temporal follows the pause and the restore.** A trigger's
   `next_fire_at` is read from its Temporal schedule and is empty exactly
   while the schedule is paused (`triggers.Syncer.NextFireAt`). After the
   delete in step 4, read the paused trigger:

   ```bash
   reliant forge db query prod --database reliant --confirm-env prod \
     --reason "post-deploy check: reliant#2890 smoke trigger state" \
     "SELECT t.id, t.enabled, t.paused_reason FROM triggers t JOIN projects p ON p.id = t.project_id
       WHERE p.name = 'rl-delete-smoke'"
   ```

   Expect `enabled = false`, `paused_reason = project_deleted`. After the
   restore, `reliant trigger get <id>` shows it enabled with a `Next fire:`
   time again (the schedule resumed). Before the restore the trigger is in a
   hidden project and is not readable through `reliant trigger get`; that is
   the database read's purpose.

## When a check fails

| Check | Failure | Recovery |
|-------|---------|----------|
| 2 | No `project purge: running` line | The worker runs an older release, or the loop failed to start: `reliant forge obs logs prod --service reliant-temporal-worker --severity error --since 2h`. Scheduled projects are only retained longer, nothing is lost; fix forward. |
| 2 | `could not purge a project` warnings | Each names a `project_id`; the pass resumes it every 15 minutes (its claim's 1h lease). Read the error; a lock or statement timeout clears on its own. A project that keeps failing stays hidden and unrestorable; its owner can retry with `reliant project delete --now <id>`. |
| 3 | Fewer than six triggers, or `tgenabled` not `O` | The migration did not apply (the API server owns migrations): `reliant forge obs logs prod --service reliant-api-server "migration" --since 2h`. Until it applies, a write racing a delete can leave a trigger on in a deleted project. The handler checks still refuse writes to a hidden project. Redeploy to re-run the migration (its 5s lock timeout fails it rather than blocking). |
| 3 | `indisvalid = false` | `DROP INDEX CONCURRENTLY idx_projects_delete_after` and re-run the migration's statement (`IF NOT EXISTS` otherwise keeps the invalid one). |
| 4 | The delete is immediate (no restore date) | The API server is on the previous release; check the release in force (`reliant forge obs status prod`). |
| 4 | A new chat or trigger is admitted in the deleted project | A blocker: delete that chat or trigger, restore the project, and roll back (below). |
| 5 | `enabled = true` after the delete | A blocker, as above. |
| 5 | Row paused, but `next_fire_at` set after the delete, or empty after the restore | The schedule did not converge (a Temporal outage at the time). The API server's startup sync (`TriggerScheduleSync`) repairs it; until then a paused trigger's fires skip on its row. `reliant trigger disable <id>` then `enable <id>` converges one by hand. |

## Rollback

The previous release ignores `delete_after` (the migration is
`forge:backward-compatible`), so after a rollback:

- **Every project scheduled for deletion reappears**, with its chats, and its
  triggers stay paused (`paused_reason = 'project_deleted'`, which the
  previous release shows as a plain pause). Nothing is purged while it runs.
- So before rolling back, either restore the scheduled projects, or accept
  that they reappear. List them:

  ```bash
  reliant forge db query prod --database reliant --confirm-env prod \
    --reason "rollback prep: reliant#2890 scheduled projects" \
    "SELECT id, user_id, delete_after, purge_started_at FROM projects WHERE delete_after IS NOT NULL ORDER BY delete_after"
  ```

  A project with `purge_started_at` set is part-purged: tell its owner it
  will reappear missing some chats, or let the purge finish first (the loop
  resumes it within the hour).
- The admission triggers stay, and still refuse writes to a project with
  `delete_after` set. Under the previous release that is only the projects
  listed above, whose owners can no longer restore them from the app. Restore
  them first, or clear the column by a migration in a fix-forward PR.

Then roll back as usual: revert this PR on `main` in a PR labelled
`release:asap`, or re-promote prod's previous reliant release
(`reliant forge env releases list prod` names it). Migrations are
forward-only; the columns, index and triggers stay.
