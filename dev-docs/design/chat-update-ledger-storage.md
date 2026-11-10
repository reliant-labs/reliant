# Where the update ledgers live: Postgres, NATS JetStream, or both

Status: **for the owner's decision.** Retention inside Postgres (option 1
below) is implemented in the same PR as this document, because the owner
approved it on 2026-11-10; it ships off and is enabled per environment.
Moving any of this to NATS is not implemented.

The question, from the owner: prod Cloud SQL `reliant-pg` went from 11 GB to
33 GB of data in 14 days, `chat_updates` and `step_executions` have never had a
row deleted, and memory sits at 83%. "Is Postgres even the right place for this
data in the first place? Or should it also use NATS?"

**Recommendation, in five lines.** Keep the ledgers in Postgres. Their value is
that a sequence number, the domain write it describes and the snapshot built
from both are one transaction in one database. JetStream would split that into
a dual write with a gap path, and it would save almost nothing: the bytes were
retention, not the engine. Compact in place now (this PR). If the table keeps
outgrowing the compaction, the next step is a heads table plus a time-partitioned
log inside Postgres, not a second store.

## What the two tables are

### `chat_updates`: a chat's sequenced change log

One row per state change a chat's clients must see: `(chat_id,
sequence_number)` is unique, the sequence comes from
`update_stream_counters` in the same transaction as the domain write
(`store.AllocateUpdateSequence`; `store.SaveMessageAtomic` writes a message and
its update in one statement), so **sequence order is commit order**. After the
commit, `Repo.onChatUpdate` publishes the row on core NATS (`chat.updates.<id>`,
`internal/streaming/nats_update_hub.go`). Core NATS, deliberately: "these
events are already durable in the database. NATS is purely the real-time
notification channel."

Readers, all in `internal/store/chat_update_repo_ext.go` and
`internal/handlers/streaming`:

| Reader | What it takes from the log |
|---|---|
| Live stream (`StreamChatUpdates`) | Every row as it is published; a jump in sequence is backfilled from Postgres. |
| Reconnect replay (`sendChatUpdateBatches`) | `(since_seq, latest]`, 500 rows per read, when the gap is at most `maxChatReplayGap` (2,000). |
| Snapshot (`buildChatSnapshot`) | The high-water mark (`MAX(sequence_number)`), then the **heads**: the newest update per entity (`SnapshotHeadsSQL`, a skip scan over `idx_chat_updates_snapshot_entity_heads`), and the newest THREAD / QUESTION per re-key (`idx_chat_updates_snapshot_rekey_heads`). Messages and tool calls come from their own tables, not from the log. |
| Run-status view (`GetWorkflowNodeExecutions`) | The newest NODE_EXECUTION per node of one workflow (`idx_chat_updates_workflow_heads`). |
| Scroll-back (`ListMessages`) | The ERROR attempts inside a page's time range (`idx_chat_updates_errors_created`). |
| Approval sweep (`ListUnannouncedApprovalResolutions`) | The newest APPROVAL row per approval. |
| Polling followers (`GetChatUpdates`, `sdk/go/reliant/follow`) | Replay from a cursor; a run's verdict is its WORKFLOW_STATUS rows. |

So the log is two things at once: a **replay buffer** (only the recent tail is
ever replayed: a gap over 2,000 rows already gets a snapshot) and a
**latest-value store** (heads per entity, per node, per approval).

### `user_updates`: the same for a user's global stream

Same shape (`(user_id, sequence_number)`, the same allocator). The user stream
replays `(since_seq, latest]` in full and has **no snapshot**: a web client
reloads its lists after any disruption instead (`globalUpdatesStore`,
`handleStatusChange`).

### `step_executions`: per-attempt step records of a run

One row per activity attempt, written by the workflow runtime
(`InsertStepExecution`). It is not a stream at all: nothing replays it.
Readers: the chat timeline (`GetWorkflowExecutions` BASIC, a handful of
user-facing rows per run), the builder's Run tab and the FULL view (every
row), and run diagnostics (`get_run`, run health, automation failure
messages: `ListRunNodeExecutions`). Its bulk is CallLLM / ExecuteTools /
SaveMessage output stored out of line.

## Measured (prod `reliant_cp`, read-only, 2026-11-10)

| | rows | total | heap | out of line | indexes |
|---|---|---|---|---|---|
| `chat_updates` | 7.76M | 10 GB | 4.5 GB | 1.6 GB | **4.4 GB** |
| `step_executions` | 2.69M | 8.9 GB | 2.5 GB | **4.8 GB** | 1.6 GB |
| `user_updates` | 1.05M | 0.8 GB | 0.3 GB | — | 0.5 GB |

`chat_updates` by type (0.1% sample): NODE_EXECUTION 54%, MESSAGE 17%,
TOOL_CALL 17%, STREAM_FINALIZED 8%, REFETCH 1.4%, WORKFLOW_STATUS 1.2%, the
rest under 1% each. 62% of rows are older than 7 days. Write rate: about
430k rows a day (5/s on average, bursty with agent loops). `step_executions`:
about 140k rows a day; CallLLM, ExecuteTools and SaveMessage are 90% of rows;
the oldest row is 2026-10-08, so a 30-day window removes only ~3% today.

The disk is not a cost problem: 33 GB of Cloud SQL SSD is about $6 a month.
What it costs is memory: the hot indexes compete for 7.5 GB of RAM with
everything else, and a mass of dead history makes every skip scan and vacuum
slower. Note also that **Cloud SQL storage never shrinks**: the 45 GB the
auto-resize reached stays provisioned whatever is deleted.

## Options

### 1. Postgres, with compaction and retention (implemented)

The log keeps everything for 7 days (configurable), then compacts: a reader
that reads heads keeps reading the same heads, and rows no reader needs after
delivery go. `step_executions` keeps a finished run's timeline rows and loses
its internal ones after 30 days.

- **Reconnect and gaps.** Each chat gets a compaction watermark
  (`update_stream_counters.compacted_through_seq`). A cursor below it gets a
  snapshot instead of a replay, and every replay batch is verified against it
  before it is sent: a batch the watermark overtook is replaced by a snapshot.
  Polling readers (`GetChatUpdates`) get the watermark in the response. Clients already handle a snapshot at any point, so
  no client changes. The watermark is raised before any row under it is
  deleted, so it never under-states what is gone.
- **Isolation.** Unchanged: rows are keyed by chat, access is checked in the
  handler.
- **Durability and backup.** Unchanged: Cloud SQL backups and PITR (7 days).
- **Ops.** One more background loop in the worker, with a metric
  (`reliant.ledger_retention.rows_deleted`) and logs. No new infrastructure.
- **Compatibility.** Additive column and table; the previous release ignores
  both. Older clients and daemons see only snapshots where they used to see a
  replay with holes they could not detect.
- **Ceiling.** Deleting rows makes dead tuples. Autovacuum reclaims them for
  reuse, but indexes stay their size until reindexed, and the delete rate is
  the write rate. At 10x today's traffic this is still small; at 100x the
  delete-then-vacuum churn becomes the cost, which is what 1b fixes.

#### 1b. Later, if needed: a heads table plus a partitioned log

Split the two jobs. A `chat_update_heads` table holds the newest update per
reader key, upserted in the same transaction as the log row; the snapshot
reads it directly (one row per key, no skip scans). `chat_updates` becomes a
pure replay buffer, range-partitioned by day or week, and retention is `DROP
TABLE` of an old partition: no deletes, no dead tuples, no vacuum, and the
space is free at once. The costs: an upsert per update on a hot row (HOT
updates mostly), a one-time backfill and swap of a 10 GB table, and a uniqueness
constraint that must include the partition key (the allocator already makes
sequences unique). Worth it only if compaction measurably costs; today it
does not.

### 2. NATS JetStream as the update log

We run a 3-node JetStream cluster in prod (R3, file store, 5 Gi per node;
`control-plane/deploy/kcl/lib/infra.k`). Today it carries the daemon events
stream (7 days), the 5-minute streaming-delta buffer (memory storage), queued
delivery and daemon routing. The chat and user update hubs use **core** NATS,
not JetStream.

A stream per tenant scope (`chat.updates.<chat>`) with a max age would be the
replay buffer; a KV bucket (last value per subject) could hold heads.

- **Correctness on reconnect.** The sequence is the problem. Today the number
  is allocated in the transaction that writes the message, so a client that
  sees sequence N has every committed change up to N. A JetStream stream
  sequence is assigned by the broker at publish time, after the Postgres
  commit, so the two can disagree: a crash between commit and publish loses an
  update (it needs an outbox to be republished), and a retry duplicates one
  (it needs a dedup window keyed by our own id). Per-subject sequences in a
  shared stream are not contiguous, so "a jump means a gap" (the backfill the
  stream does today) would need our own sequence in a header anyway, which
  still comes from Postgres.
- **The snapshot.** It is a consistent read across messages, tool calls,
  threads and the heads at one high-water mark. With heads in KV, that is a
  read across two systems with no common snapshot: a head newer than the
  messages read, or older, is exactly the class of bug the round ordering in
  `buildChatSnapshot` exists to prevent. And our heads are not one key per
  subject: the snapshot keys by entity, re-keys threads and questions,
  run-status views key by node, errors are read by time range. That is several
  KV buckets with their own consistency.
- **Isolation.** NATS subjects are isolated by account and permissions
  (`deploy/nats/permissions.yaml`), which today are per workload, not per
  tenant. All reads would still go through the API server, so this is
  workable, but it is a second authorization surface.
- **Durability and backup.** Raft-replicated, with no backup or PITR. A lost
  cluster loses the log; today that is acceptable only because the log is a
  notification channel.
- **Ops.** Sizing the cluster's disk (5 Gi per node now) to hold 7 days of
  every chat's updates, stream limits per tenant, a consumer per open stream.
- **Compatibility.** The wire protocol could stay (the server would translate),
  but every writer of `chat_updates` would change, and both stores would run
  side by side through a migration.
- **Cost.** Similar bytes, now on three replicated PVCs instead of one database.

### 3. Hybrid: Postgres is the system of record, JetStream a replay cache

Keep everything as it is, and also publish each committed update to a
JetStream stream with a short max age; reconnects replay from JetStream when
the cursor is inside it and fall back to Postgres otherwise. This is the cheap
version of 2: no correctness moves to NATS, because Postgres stays
authoritative and a gap falls back. But it buys little: replay is already a
bounded read (at most 2,000 rows, 500 per round trip) and is not where the
database's time goes, and every published update is written twice.

## Recommendation, and the case against it

**Option 1 now (shipped), 1b if growth outruns it; not 2 or 3.**

The case against: Postgres is being asked to be a message log, which is
exactly what JetStream is built for, and the work in this PR (watermarks,
reader-by-reader keep rules, batched deletes) is work a log store gives for
free with max-age. That is true of the replay buffer half. It is not true of
the half that matters: the sequence is meaningful only because it is
allocated in the domain write's transaction, and the snapshot is correct only
because it reads heads and domain rows from one database. Both stores
would be needed anyway, plus the outbox between them. If the retention logic
ever feels like the wrong tool, 1b removes it (drop a partition) without
splitting the source of truth.

The other risk is that compaction is subtle: a keep rule that misses a
reader loses state silently. The implementation keeps a superset of each
reader's keys, keeps the rarer history-shaped types whole (THREAD,
QUESTION, ERROR, WORKFLOW_STATUS: about 2% of rows), and its tests hold every
reader to the exact rows it read before compaction.

## Open, for the owner

1. **Turning it on.** Both retention periods default to 0 (off): this release
   ships the watermark-aware readers, and an environment enables deletion
   explicitly once every api-server it runs is on a release that has them.
   Recommended: chat updates 7 days (`RELIANT_WORKER_CHAT_UPDATE_RETENTION=168h`),
   step records 30 days (`RELIANT_WORKER_STEP_EXECUTION_RETENTION=720h`). At 30
   days `step_executions` stops growing at about its current size; 14 days would
   roughly halve it. The PR body has the enablement, acceptance check and stop
   procedure.
2. **`user_updates`** is not compacted (0.8 GB). Its stream has no snapshot to
   fall back to, so pruning it needs a resync signal on the user stream first.
   Not worth doing at its size today.
3. **Reclaiming space** after the first pass (see the PR): Postgres reuses the
   freed space inside each table, so both stop growing; the files do not
   shrink. `REINDEX INDEX CONCURRENTLY` on the `chat_updates` indexes (4.4 GB)
   is what gives memory back; `pg_repack` (available on Cloud SQL) would
   shrink the heaps too. Neither returns disk to Cloud SQL, which never
   shrinks.
