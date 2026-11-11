# Redundant index drops (2026-11-10): post-deploy check and rollback

Six migrations drop `chat_updates`, `messages` and `message_content_blocks`
indexes that prod's `pg_stat_user_indexes` showed as duplicates or unread
(about 2.3 GB of index, and an index write on every insert):

| Migration | Index dropped | What serves its readers now |
|-----------|---------------|-----------------------------|
| `20261110160125_drop_chat_updates_chat_seq_index` | `idx_chat_updates_chat_seq` | `chat_updates_chat_sequence_key` (unique, same columns) |
| `20261110160152_drop_chat_updates_snapshot_heads_index` | `idx_chat_updates_snapshot_heads` | `idx_chat_updates_snapshot_entity_heads` |
| `20261110160218_drop_chat_updates_snapshot_rekeyed_index` | `idx_chat_updates_snapshot_rekeyed` | `idx_chat_updates_snapshot_rekey_heads` |
| `20261110160247_drop_chat_updates_created_index` | `idx_chat_updates_created` | none needed; `idx_chat_updates_errors_created` serves the one `created_at` read |
| `20261110160304_drop_messages_thread_ordinal_index` | `idx_messages_thread_ordinal` | `messages_thread_ordinal_key` (unique, same columns) |
| `20261110160321_drop_content_blocks_activity_index` | `idx_content_blocks_activity` | none needed (no reader) |

A planner regression from a dropped index only shows on prod, after the
release that carries these migrations is deployed. This is how it is detected,
and how it is undone.

## After deploy: the check

Run these once the release is live in prod, and again about a day later (the
index statistics need traffic to move).

1. **The release is healthy.** `reliant forge obs check prod --wait --json`,
   branching on its exit code as usual (0 good; 1 regressed: read its
   `suggested[]`; 2 not proven good; 8 check again later). Compare the chat-open
   latency (the `GetChat` / snapshot RPC p95, `reliant forge obs status prod`)
   against its value from the day before the deploy: it must not rise.
2. **The indexes are gone, and the replacements carry the reads.**

   ```bash
   reliant forge db query prod --database reliant --confirm-env prod \
     --reason "post-deploy check: redundant index drops" \
     "SELECT indexrelname, idx_scan FROM pg_stat_user_indexes
       WHERE indexrelname IN ('idx_chat_updates_chat_seq','idx_chat_updates_snapshot_heads',
         'idx_chat_updates_snapshot_rekeyed','idx_chat_updates_created',
         'idx_messages_thread_ordinal','idx_content_blocks_activity',
         'chat_updates_chat_sequence_key','idx_chat_updates_snapshot_entity_heads',
         'idx_chat_updates_snapshot_rekey_heads','idx_chat_updates_errors_created',
         'messages_thread_ordinal_key')"
   ```

   None of the six dropped names may be listed. Between the two runs,
   `idx_scan` must grow on `chat_updates_chat_sequence_key` (MAX(sequence_number),
   `ChatUpdatesSince`), `idx_chat_updates_snapshot_entity_heads` (`SnapshotHeadsSQL`,
   the approval sweep's `listUnannouncedResolutionsSQL`),
   `idx_chat_updates_snapshot_rekey_heads` (the snapshot's re-keyed half) and
   `messages_thread_ordinal_key` (message ordinal reads).
3. **No sequential scan crept in.** In the same query form,
   `SELECT relname, seq_scan, idx_scan FROM pg_stat_user_tables WHERE relname IN
   ('chat_updates','messages','message_content_blocks')`: `seq_scan` on these
   tables must not grow faster than it did before the deploy. A planner that
   lost its index on these tables falls back to a sequential scan, which this
   shows.
4. **Plans, if any of the above moved.** Re-run the plans the tests pin on a
   real chat, in a read-only session on the prod `reliant` database:
   `EXPLAIN` of `store.SnapshotHeadsSQL`, `listUnannouncedResolutionsSQL`
   (`internal/store/approval_repo_ext.go`), `store.ErrorUpdatesInRangeSQL`, and
   `ChatUpdatesSince`'s `WHERE chat_id = $1 AND sequence_number > $2 ORDER BY
   sequence_number LIMIT $3`. Each must name one of the indexes in the table
   above, not a `Seq Scan` on `chat_updates`.

## Rollback: recreate the index, fix-forward

Migrations are forward-only, so a drop is undone by a new migration
(`task db:migration NAME=recreate_<index>`) that recreates the index from its
original definition (`db/migrations/20261014050404_baseline.up.sql`). `CREATE
INDEX CONCURRENTLY` cannot run in a transaction, so each is the only statement
in its own file, marked `-- forge:backward-compatible — recreates an index; no
reader or writer depends on its absence.` Recreate only the index whose reads
regressed:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_updates_chat_seq ON public.chat_updates USING btree (chat_id, sequence_number DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_updates_snapshot_heads ON public.chat_updates USING btree (chat_id, entity_id, sequence_number DESC) WHERE (update_type <> ALL (ARRAY[1, 4, 19]));
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_updates_snapshot_rekeyed ON public.chat_updates USING btree (chat_id, sequence_number) WHERE (update_type = ANY (ARRAY[3, 18]));
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_updates_created ON public.chat_updates USING btree (created_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_thread_ordinal ON public.messages USING btree (thread_id, ordinal);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_blocks_activity ON public.message_content_blocks USING btree (activity_id, workflow_run_id);
```

Ship it as a `prod:fix-forward` PR labelled `release:asap`. A build of
`chat_updates` index takes minutes on prod's table and blocks no reads or
writes; a failed `CONCURRENTLY` build leaves an `INVALID` index, which the
same migration's `IF NOT EXISTS` would then skip, so check
`pg_index.indisvalid` after it runs and `DROP INDEX CONCURRENTLY` an invalid
one before re-running.
