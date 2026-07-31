-- +goose Up
-- Thread origin: how a thread came to exist, stored on the thread itself.
--
-- Previously "is this a spawn?" was answered by string-comparing a workflow
-- row's spawned_by_node_id against the magic value 'spawn_tool'. That field
-- has two incompatible jobs: for a workflow-node child it holds a real node
-- ID (provenance — "which node produced this"), and for a spawn-tool child it
-- held a type tag. Kind and provenance are different questions, and answering
-- the first with a field that stores the second is why the timeline could not
-- distinguish a spawn thread from the "thread:<node>" lifecycle record that
-- shares its thread ID: both rows have a non-empty spawned_by_node_id, so no
-- truthiness check can separate them.
--
-- origin answers the kind question and lives on threads, which already owns
-- every other piece of thread identity (parent_thread_id, fork_at_ordinal,
-- title). origin_node_id keeps the provenance half for the cases where a
-- graph node really did create the thread.
--
-- Values:
--   'main'  — the chat's root thread; no parent
--   'spawn' — created by the spawn tool (was: spawned_by_node_id='spawn_tool')
--   'fork'  — forked from a parent thread at an ordinal (has fork metadata)
--   'node'  — created by a workflow graph node (origin_node_id names it)
ALTER TABLE threads ADD COLUMN IF NOT EXISTS origin TEXT;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS origin_node_id TEXT;

-- Lifecycle state for threads. This is the other half of why "thread:<node>"
-- workflow rows existed: threads had identity but no lifecycle, so the
-- executor synthesized fake workflow rows to record thread start/finish.
-- Statuses mirror CHAT_WORKFLOW_STATUS (2=running, 3=completed, 4=failed,
-- 5=cancelled, 7=expired) so a thread's state is comparable to its workflow's
-- without a translation table.
ALTER TABLE threads ADD COLUMN IF NOT EXISTS status INTEGER NOT NULL DEFAULT 2;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

-- ---------------------------------------------------------------------------
-- Backfill. The DB is small enough that one pass under the implicit DDL lock
-- is fine; no batching, no concurrent index build.
--
-- Order matters: each statement below only fills rows still NULL, so the
-- earlier (more specific) classifications win over the later fallbacks.
-- ---------------------------------------------------------------------------

-- 1. Spawn threads. The old truth was a workflow row for this thread carrying
--    the 'spawn_tool' sentinel. Read it one last time, then never again.
--    origin_node_id stays NULL: 'spawn_tool' was never a node ID.
UPDATE threads t
SET origin = 'spawn'
WHERE t.origin IS NULL
  AND EXISTS (
      SELECT 1 FROM workflows w
      WHERE w.thread = t.id
        AND w.spawned_by_node_id = 'spawn_tool'
  );

-- 2. Forks. Fork metadata on the thread is self-describing — it never needed
--    the workflows table to begin with.
UPDATE threads
SET origin = 'fork'
WHERE origin IS NULL
  AND fork_at_ordinal IS NOT NULL;

-- 3. Node-created threads. Any remaining thread whose workflow row names a
--    real graph node. Excludes the 'thread:'/'fork:' lifecycle records, whose
--    spawned_by_node_id is the *emitting* node and would otherwise overwrite
--    a more accurate answer; also excludes the spawn sentinel, already handled
--    in step 1. Here spawned_by_node_id IS provenance, so it carries over.
UPDATE threads t
SET origin = 'node',
    origin_node_id = (
        SELECT w.spawned_by_node_id FROM workflows w
        WHERE w.thread = t.id
          AND w.spawned_by_node_id IS NOT NULL
          AND w.spawned_by_node_id <> 'spawn_tool'
          AND w.workflow_name NOT LIKE 'thread:%'
          AND w.workflow_name NOT LIKE 'fork:%'
        ORDER BY w.created_at ASC
        LIMIT 1
    )
WHERE t.origin IS NULL
  AND EXISTS (
      SELECT 1 FROM workflows w
      WHERE w.thread = t.id
        AND w.spawned_by_node_id IS NOT NULL
        AND w.spawned_by_node_id <> 'spawn_tool'
        AND w.workflow_name NOT LIKE 'thread:%'
        AND w.workflow_name NOT LIKE 'fork:%'
  );

-- 4. Root threads. No parent means the chat's main thread.
UPDATE threads
SET origin = 'main'
WHERE origin IS NULL
  AND parent_thread_id IS NULL;

-- 5. Anything left has a parent but no surviving evidence of how it was made.
--    A child thread of unknown provenance is far more likely a spawn than a
--    graph node (the spawn tool is the common producer of parented threads),
--    but guessing 'spawn' would fabricate specificity we do not have. 'node'
--    with a NULL origin_node_id is the honest encoding: created by something
--    in the graph, and we no longer know what.
UPDATE threads
SET origin = 'node'
WHERE origin IS NULL;

ALTER TABLE threads ALTER COLUMN origin SET NOT NULL;

-- Backfill thread lifecycle from the records that used to carry it. A thread
-- whose "thread:<node>" row reached a terminal status inherits that status and
-- timestamp; everything else keeps the default (2=running) and is reconciled
-- by the normal workflow-status path.
UPDATE threads t
SET status = w.status,
    completed_at = w.completed_at
FROM workflows w
WHERE w.thread = t.id
  AND w.workflow_name LIKE 'thread:%'
  AND w.status <> 2;

-- The lifecycle records have now been drained into threads. Deleting them is
-- what removes the 'thread:%' prefix-sniffing from every consumer: with these
-- rows gone, a workflow row is always a real workflow execution.
--
-- 'fork:%' is included for completeness — nothing has written that prefix for
-- some time (no Go code produces it; only two frontend predicates still tested
-- for it), so this is expected to delete zero rows on any recent database.
--
-- Safe with respect to the FK: workflows.parent_id is ON DELETE SET NULL, and
-- these records are always leaves (nothing is parented to a thread record).
DELETE FROM workflows
WHERE workflow_name LIKE 'thread:%'
   OR workflow_name LIKE 'fork:%';

CREATE INDEX IF NOT EXISTS idx_threads_origin ON threads(conversation_id, origin);

-- +goose Down
-- Irreversible in the strict sense: the deleted lifecycle records cannot be
-- reconstructed (their workflow IDs were derived from Temporal-side state).
-- Dropping the columns restores the old schema shape, which is what a rollback
-- needs; readers fall back to the spawned_by_node_id sentinel, which was never
-- removed from workflows.
DROP INDEX IF EXISTS idx_threads_origin;
ALTER TABLE threads DROP COLUMN IF EXISTS completed_at;
ALTER TABLE threads DROP COLUMN IF EXISTS status;
ALTER TABLE threads DROP COLUMN IF EXISTS origin_node_id;
ALTER TABLE threads DROP COLUMN IF EXISTS origin;
