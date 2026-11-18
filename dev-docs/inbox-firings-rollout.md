# ListInbox read bounds (reliant#2736): post-deploy check and rollback

reliant#2736 changes three of the Inbox's reads and adds one index:

| Change | Where |
|--------|-------|
| `FiringsSinceLastSuccess` looks for the newest success only among the newest `?2` firings, bounds the episode by an index condition, and probes each firing's chat by id instead of joining `chats_with_activity` | `internal/store/trigger_event_repo_ext.go` |
| `triggerOutcomeColumns` really nulls `payload` / `sender` / the other event bodies | same file |
| `ListInboxPending`'s paused branch starts from the paused root runs (`idx_workflows_state`), not from every one of the caller's chats | `internal/store/inbox_dismissal_repo_ext.go` |
| `idx_chats_daemon_blocked`, a partial index on `chats (user_id) WHERE daemon_blocked_at IS NOT NULL`, for the waiting-for-machine branch | `db/migrations/20261117205305_chats_daemon_blocked_index.up.sql` |

The plan tests (`internal/db/inbox_pending_plan_test.go`,
`internal/db/trigger_firings_plan_test.go`) pin the plans on a seeded
database. Only prod has prod's cardinalities, so the latency effect is
checked there, after the release that carries this change is deployed.

## After deploy: the check

Run these once the release is live in prod, and again about 6 hours later.

1. **The release is healthy.** `reliant forge obs check prod --wait --json`,
   branching on its exit code as usual (0 good; 1 regressed: read its
   `suggested[]`; 2 not proven good; 8 check again later).
2. **ListInbox got faster, not slower.**
   `reliant forge obs traces prod --route ListInbox --since 6h`. Compare its
   p95 with the same window the day before the deploy. It must not rise.
3. **The index built and is used.**

   ```bash
   reliant forge db query prod --database reliant --confirm-env prod \
     --reason "post-deploy check: idx_chats_daemon_blocked" \
     "SELECT i.indisvalid, s.idx_scan > 0 AS used FROM pg_index i
        JOIN pg_stat_user_indexes s ON s.indexrelid = i.indexrelid
       WHERE i.indexrelid = 'public.idx_chats_daemon_blocked'::regclass"
   ```

   `indisvalid` must be true. An INVALID index (a failed concurrent build) is
   dropped with `DROP INDEX CONCURRENTLY idx_chats_daemon_blocked` and rebuilt
   by re-running the migration's statement; `IF NOT EXISTS` would otherwise keep
   the invalid one. That is a fix-forward of the index alone and needs no
   application rollback.
4. **The Inbox still lists the same items.** Open the Inbox as a user with
   automations: paused automation runs, failing-automation episodes (with their
   firing count) and waiting-for-machine runs are listed as before the deploy.

## Failure criteria

Roll back the application (below) if any of these holds:

- `obs check` exits 1 and its `suggested[]` issues name `ListInbox`,
  `InboxService` or the trigger-event reads;
- `ListInbox` p95 over the 6h after the deploy is higher than the same window
  the day before;
- `ListInbox` returns errors it did not return before the deploy;
- the Inbox drops or invents items (step 4): a paused run, an episode, or a
  firing count that differs from the trigger's firing history.

## Rollback: redeploy the previous release

Every change above is a read; nothing writes new data or depends on the
index, so the previous release's reads work unchanged against this schema.

1. Revert this PR's commit on `main` in a PR labelled `release:asap`. The
   pipeline reviews and merges it, and `rc-train` cuts the release right after,
   which redeploys the API server with the previous statements. If prod cannot
   wait for that, re-promote prod's previous reliant release instead (releases
   are promoted by digest, so it is still in the registry;
   `reliant forge env releases list prod` names it).
2. Re-run checks 1 and 2 against the rolled-back release: `ListInbox` p95 must
   be back at its pre-deploy value.
3. **Leave `idx_chats_daemon_blocked` in place.** The migration is
   `forge:backward-compatible`: an extra index changes no result, the previous
   release ignores it, and as a partial index on the rare blocked chats it costs
   almost nothing to keep. Migrations are forward-only; drop it only by a new
   migration (`DROP INDEX CONCURRENTLY IF EXISTS idx_chats_daemon_blocked`, alone
   in its file) if it is ever shown to be the regression, which no read in the
   previous release can make it.
