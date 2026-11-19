# Chat list live-chat index (reliant#2945): post-deploy check and rollback

reliant#2945 adds one index and changes no Go code:

| Change | Where |
|--------|-------|
| `idx_chats_user_live_last_active`, a partial index on `chats (user_id, last_active DESC) WHERE state <> 3`, built `CONCURRENTLY` | `db/migrations/20261119170206_chats_user_live_last_active_index.up.sql` |

`store.ListChats` (the sidebar's `ListChats` and the worktree reads, with
`ExcludeArchived`) sends `state != 3 … ORDER BY last_active DESC`, which the
index answers. Before it, prod Seq Scanned `chats` for the heaviest user
(46,639 chats, 44,385 archived) and discarded 46,783 rows to return 26.

`internal/db/chats_live_index_plan_test.go`
(`TestChatListReadsLiveChatsFromTheirIndex`) pins the plan on a prod-shaped
seed under both custom and generic plans. Only prod has prod's data, so
whether the build succeeded and whether the planner adopts the index there is
checked after the release that carries the migration is deployed.

## After deploy: the check

Run these once the release is live in prod, and again about 6 hours later.

1. **The release is healthy.** `reliant forge obs check prod --wait --json`,
   branching on its exit code as usual (0 good; 1 regressed: read its
   `suggested[]`; 2 not proven good; 8 check again later).
2. **The index built and is valid.**

   ```bash
   reliant forge db query prod --database reliant --confirm-env prod \
     --reason "post-deploy check: idx_chats_user_live_last_active" \
     "SELECT i.indisvalid, i.indisready, s.idx_scan, pg_size_pretty(pg_relation_size(i.indexrelid)) AS size
        FROM pg_index i
        JOIN pg_stat_user_indexes s ON s.indexrelid = i.indexrelid
       WHERE i.indexrelid = 'public.idx_chats_user_live_last_active'::regclass"
   ```

   `indisvalid` and `indisready` must both be true. The size is of the live
   chats only (a few MB at most), not of every chat.
3. **ListChats adopts it.** On the second run (6h later), `idx_scan` from
   step 2 must have grown: every sidebar load of a user with a live chat reads
   it. To see the plan itself, run the statement the plan test pins
   (`liveChatsStatement` in `internal/db/chats_live_index_plan_test.go`) under
   `EXPLAIN (COSTS OFF)` for the heaviest user, with the views inlined if the
   role cannot read them. It must name `idx_chats_user_live_last_active` and
   must not contain `Seq Scan on chats`.
4. **ListChats got faster, not slower.**
   `reliant forge obs traces prod --route ListChats --since 6h`. Compare its
   p95 with the same window the day before the deploy. It must not rise.
5. **The sidebar lists the same chats.** Open the app as a user with archived
   automation runs: the sidebar shows the same live chats, newest first, and
   no archived one.

## Failure criteria and response

- **The build failed** (`indisvalid` false, or the migration job errored): a
  failed `CREATE INDEX CONCURRENTLY` leaves an INVALID index that `IF NOT
  EXISTS` would keep skipping, and the previous statements keep working
  without it (they read no index by name). Fix forward on the index alone,
  with no application rollback:

  ```sql
  DROP INDEX CONCURRENTLY IF EXISTS idx_chats_user_live_last_active;
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chats_user_live_last_active
    ON public.chats (user_id, last_active DESC) WHERE state <> 3;
  ```

  (each statement on its own, outside a transaction), then re-run step 2.
- **The index is valid but unused** (step 3: `idx_scan` does not grow, or the
  plan still Seq Scans `chats`): nothing is worse than before the deploy;
  open an issue with the plan. No rollback.
- **Roll back the application** (below) if `obs check` exits 1 naming
  `ListChats` or `ChatService`, if `ListChats` p95 over the 6h after deploy is
  higher than the day before, if `ListChats` returns errors it did not before,
  or if the sidebar drops or invents chats (step 5).

## Rollback: redeploy the previous release

The migration is `forge:backward-compatible`: an extra index changes no
result, and the previous release ignores it.

1. Revert this PR's commit on `main` in a PR labelled `release:asap`; the
   pipeline reviews and merges it and `rc-train` cuts the release right after.
   If prod cannot wait, re-promote prod's previous reliant release by digest
   (`reliant forge env releases list prod` names it).
2. Re-run checks 1 and 4 against the rolled-back release.
3. **Leave `idx_chats_user_live_last_active` in place.** Migrations are
   forward-only; drop it only by a new migration
   (`DROP INDEX CONCURRENTLY IF EXISTS idx_chats_user_live_last_active`, alone
   in its file) if it is ever shown to be the regression, which no read of the
   previous release can make it.
