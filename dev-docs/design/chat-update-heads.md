# Materialized chat update heads

Status: accepted (owner, 2026-11-10). Implemented in two PRs: the table, the
dual write and the read with its fallback; then the backfill.

## Problem

Every chat snapshot (`getNonMessageUpdates`, internal/handlers/streaming/
streaming.go) reads the chat's *update heads*: for each entity, its newest
non-message `chat_updates` row (`store.SnapshotHeadsSQL`). That statement is a
recursive skip scan over `chat_updates`, which on prod is 11 GB (8.1M rows,
4.7 GB heap, ~4.6 GB of indexes) on a database with 2.4 GB of shared_buffers.
Warm, it takes 0.25-9 ms. Cold, each probe is a random read: 55-139 disk reads
and 53-189 ms on a typical chat, up to 9.5 s on a large one. In the 72 h
measured, 21 of the 23 server initial syncs over 1.5 s were at least 95% this
one query.

The skip scan is already as good as a read of the log can be: one index probe
per entity. What it cannot fix is that the heads are scattered through a table
far larger than memory, so a chat opened for the first time in a while pays
one cold read per head.

## Decision

Keep the heads themselves in a small table, written in the same transaction as
the log row, so the snapshot reads one index range of ~50-150 rows that sit
together on a few pages.

### Alternatives considered

- **Retention only** (prune superseded `chat_updates` rows). It shrinks the
  table and so raises the hit rate, but the snapshot still walks one probe per
  entity through a table of every chat's recent history, and the pruning job
  itself has to compute heads to know what it may delete. It is the follow-up
  this change makes possible, not a substitute for it.
- **JetStream as the source of snapshot state** (a KV bucket per chat, or
  last-per-subject on the chat stream). It moves the durable truth out of
  Postgres, so the "same transaction as the log row" guarantee becomes a
  dual-write across two systems with its own reconciliation story, and the
  replay/snapshot contract (`sequence_number` order equals commit order) would
  have to be re-proved. Too large a change for a read-latency problem.
- **Partitioning `chat_updates`** (by chat hash or by time). By hash it does not
  change the number of cold pages a snapshot touches; by time it helps
  retention but scatters an old chat's heads across partitions. Either is a
  rewrite of an 11 GB table under live writes.
- **This design** (owner's choice): a derived table maintained in the write
  transaction. Bounded by entities, not history; no new system; the log stays
  the source of truth and the fallback.

## Schema

```sql
CREATE TABLE chat_update_heads (
    chat_id          text        NOT NULL,
    head_key         text        NOT NULL,
    update_id        text        NOT NULL,  -- chat_updates.id of the head row
    sequence_number  bigint      NOT NULL,
    update_type      integer     NOT NULL,
    entity_id        text        NOT NULL,
    data             text        NOT NULL,
    created_at       timestamptz NOT NULL,
    PRIMARY KEY (chat_id, head_key)
);
ALTER TABLE update_stream_counters
    ADD COLUMN heads_through_seq bigint NOT NULL DEFAULT 0;
```

`head_key` is the identity the snapshot dedups on, namespaced so the two halves
of `SnapshotHeadsSQL` can never collide in one primary key:

- `'e:' || entity_id` for the entity-keyed types (every type except 1, 3, 4,
  5, 9, 18, 19, 20 — the literal list `SnapshotHeadsSQL` and
  `idx_chat_updates_snapshot_entity_heads` share);
- `'r:' || SnapshotRekeyKeySQL` for THREAD (3) and QUESTION (18), computed by
  the same expression, character for character, in SQL.

`SnapshotHeadsSQL` keeps the two halves apart (a UNION of sequence numbers), so
an entity id that happened to equal a re-key key is two heads there; the
prefixes keep it two heads here. The table has no foreign key to `chats`, like
`chat_updates`: a write must never fail here where the log insert would succeed.
It is `forge:no-entity`: nothing may write it except through the one statement
below, which is what keeps "highest sequence wins".

## Write path

There are exactly two places a `chat_updates` row is inserted, and both
allocate its sequence from `update_stream_counters` in the same transaction:

1. `store.AppendChatUpdate` (`AllocateUpdateSequence`), behind
   `Repo.CreateChatUpdate` and so every emitter in internal/db
   (chat_update_emitters.go, refetch.go, approval_close.go, node execution) and
   every activity, handler and service that calls `CreateChatUpdate`;
2. `store.SaveMessageAtomic` (`saveMessageAtomicSQL`'s `upd` CTE), the message
   writer.

Both now:

- advance the watermark in the allocation itself:
  `heads_through_seq = CASE WHEN heads_through_seq = last_assigned_seq THEN
  last_assigned_seq + 1 ELSE heads_through_seq END` (a counter row this release
  creates starts complete, at 1);
- for an entity-keyed or re-keyed type, apply the new log row to the heads
  table in the same transaction (`store.ApplyChatUpdateHeads`): read the rows
  of `(lo, hi]` back from `chat_updates`, compute `head_key`, keep the newest
  per key, and upsert `... ON CONFLICT (chat_id, head_key) DO UPDATE ... WHERE
  excluded.sequence_number > chat_update_heads.sequence_number`. Reading the
  row back rather than passing values means the key expression is evaluated by
  Postgres on the stored row, exactly as `SnapshotHeadsSQL` evaluates it.

The upsert is monotone (a head only ever moves to a higher sequence), so any
interleaving of writers and the backfill converges to the same table.

### Why a watermark rather than a "backfilled" flag

The previous release keeps writing during the rollout, and again after a
rollback. Its allocation statement advances `last_assigned_seq` and leaves
`heads_through_seq` alone, and it writes no heads. So:

> `heads_through_seq = last_assigned_seq` holds exactly when every update the
> chat has was written by a release that maintains the heads table (or was
> applied by the backfill).

The first write by an old release breaks the equality for good: every later
allocation sees `heads_through_seq <> last_assigned_seq` and leaves it. A
per-chat `heads_backfilled_at` flag could not see that write and would serve a
stale head (an answered question shown pending, a finished thread shown
running). The watermark rides on a row every writer already locks and updates,
so it costs no extra statement, and the column has no index, so the update
stays HOT.

## Read path

`store.SnapshotUpdateHeads` runs one statement:

```sql
SELECT c.heads_through_seq = c.last_assigned_seq, h.*
  FROM update_stream_counters c
  LEFT JOIN chat_update_heads h
         ON h.chat_id = $1 AND c.heads_through_seq = c.last_assigned_seq
 WHERE c.stream_kind = 'chat' AND c.stream_id = $1
 ORDER BY h.sequence_number
```

One statement is one snapshot: the completeness check and the heads it vouches
for cannot straddle a write. Complete: those rows are the heads. Incomplete, or
no counter row: the chat falls back to `SnapshotHeadsSQL`, exactly today's read.
So the read never returns fewer (or staler) heads than today's.
`Repo.GetLatestNonMessageUpdatesPerEntity` sets
`chat_snapshot.update_heads.source = heads_table | log_fallback` on the
`chat_snapshot.update_heads` span.

## Backfill

Existing chats start incomplete (`heads_through_seq` defaults to 0) and read
the log until backfilled. `store.BackfillChatUpdateHeads(chat)`:

1. `SELECT last_assigned_seq ... FOR UPDATE` in its own short transaction: L0.
   Taking the row lock waits out any writer that allocated a sequence and has
   not committed, so every sequence `<= L0` is committed when it returns.
2. Without the counter lock: upsert the heads `SnapshotHeadsSQL` computes from
   the log (the skip scan, one statement). Slow on a cold chat, but it blocks
   no writer; concurrent writers upsert their own heads beside it, and the
   upsert is monotone.
3. In one short transaction holding the counter row lock: read L1, apply the
   log rows of `(L0, L1]` to the heads (written meanwhile by either release),
   and set `heads_through_seq = L1`.

Every log row `<= L1` is then reflected: those `<= L0` by step 2, the rest by
step 3, and no row above L1 can exist while step 3 holds the lock. Lock order
(counter row, then head rows) is the writers' order, so the two cannot
deadlock.

`ChatUpdateHeadsBackfill`, an api-server component like `LLMUsageRetention`,
walks `update_stream_counters` for chat streams whose watermark is behind, in
keyset batches of chat ids, one chat at a time with a pause between chats, and
starts over on a timer (every 10 minutes). It is resumable by construction: the
work list IS the state (`heads_through_seq <> last_assigned_seq`), so a
restart, a crash or a rollback-and-roll-forward simply finds what is still
behind. A session advisory lock keeps a pass to one replica at a time; that is
only to avoid duplicated work, since two backfills of one chat converge anyway
(monotone upsert; step 3 serialized by the row lock).

There is no backfill in a migration: a single transaction over 8M rows on prod
is exactly what the constraints rule out, and the per-chat procedure needs no
migration at all.

## Rollout and rollback

- **Migration** (backward-compatible): a new table and a defaulted column. The
  previous release never reads either; its counter upserts name their columns,
  so the default fills the new one.
- **Rolling deploy**: chats written by an old pod become (or stay) incomplete
  and read the log. Nothing is ever served from a heads table that missed a
  write.
- **Rollback**: the previous release reads only the log, which every release
  keeps writing. On the roll forward, chats it wrote are incomplete and the
  backfill repairs them.
- **Kill switch**: none needed for correctness; if the heads read ever
  misbehaved, a release that always takes the fallback is a one-line change and
  is exactly today's behaviour.

`accountpurge` deletes the user's `chat_update_heads` rows with their
`chat_updates`.

## Verification

- A differential test (`internal/store`, real Postgres): randomized sequences of
  updates over every type, including question re-keys, thread payloads with and
  without `thread`, entity ids that collide with re-key keys, interleaved
  writers, and sequences allocated out of commit order; after each, the heads
  table read equals `SnapshotHeadsSQL` row for row.
- Mixed-version tests: a write through the previous release's allocation
  statement makes the chat fall back; the backfill makes it complete again and
  the read then equals the log.
- The existing snapshot tests (`TestSnapshotHeadsQueryMatchesPartialIndex` and
  the `GetLatestNonMessageUpdatesPerEntity` suite) stay green unchanged.

## Follow-up: retention of `chat_updates`

Once every chat is complete, the snapshot no longer reads old log rows at all.
Replay reads only the last `maxChatReplayGap` (2000) sequences before falling
back to a snapshot (`chatCursorNeedsSnapshot`), and the other readers are
bounded too (`LatestWorkflowNodeUpdates` reads node heads; `ErrorUpdatesInRange`
reads error rows in a page's time range — both need checking before pruning
their types). So rows that are neither a head nor within the replay window
become deletable, which is most of the 11 GB (NODE_EXECUTION 54%, MESSAGE 17%,
TOOL_CALL 17%). That needs its own design: the previous release still reads
the log for snapshots, so pruning may start only once no supported release
falls back to it, and the fallback in this release must then be retired.
