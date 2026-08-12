-- +goose Up

-- Backfill tool_calls.message_id from each call's own content block.
--
-- message_id links a tool call to the assistant message that requested it, and
-- every read path enriches tool-call blocks through it:
--
--     SELECT ... FROM tool_calls WHERE message_id IN (...)
--
-- A row with message_id NULL cannot match that predicate, so its durable
-- status and (for a spawn) its child_workflow_id never reach the client. The
-- visible symptom was a running spawn whose preview said "Starting..." for the
-- entire run and then filled in correctly on reload -- wrong on the path you
-- watch, right on the path you check afterwards.
--
-- The column is NULL because only one of the writers that create these rows
-- ever had a message in hand. The two that run during a live tool call are
-- workflow-side: the spawn path's config carries toolCallID, childWorkflowID,
-- prompt and preset, and the proto ToolCallMsg it is parsed from is only
-- {id, name, input}. Neither knows the message id, and the upsert's merge only
-- preserved an existing value -- it never derived one.
--
-- Measured on the dev database when this was written: 124 spawn calls with a
-- NULL message_id, including every currently-executing one.
--
-- ---------------------------------------------------------------------------
-- Why the block is the right source
-- ---------------------------------------------------------------------------
-- The ordering guarantees the link exists: the assistant message and its
-- tool_call content block are persisted BEFORE tools execute. On live data the
-- block predates its own tool_calls row by 0.11-0.30s in every observed case,
-- and all 124 NULL rows have a block carrying a message_id. So this is a
-- lookup along an existing foreign key, not an inference.
--
-- block_type = 2 is CONTENT_BLOCK_TYPE_TOOL_CALL. Result blocks (type 3) share
-- the tool_call_id but belong to the TOOL-role message that carries the
-- result, not the assistant message that requested the call -- attributing a
-- call to that message would be wrong, so they are excluded.
--
-- DISTINCT ON with a deterministic ORDER BY handles a duplicate tool_call
-- block sharing one tool_call_id (the same anomaly 20260801010000 documents in
-- its own backfill): keep the earliest, which is the one the conversation
-- actually issued.
--
-- Only NULL rows are touched; a message_id already recorded is authoritative
-- and is never overwritten.

WITH call_blocks AS (
    SELECT DISTINCT ON (b.tool_call_id)
        b.tool_call_id,
        b.message_id
    FROM message_content_blocks b
    WHERE b.block_type = 2
      AND b.tool_call_id IS NOT NULL
      AND b.tool_call_id <> ''
      AND b.message_id IS NOT NULL
    ORDER BY b.tool_call_id, b.created_at, b.id
)
UPDATE tool_calls tc
SET message_id = cb.message_id,
    updated_at = now()
FROM call_blocks cb
WHERE tc.id = cb.tool_call_id
  AND tc.message_id IS NULL;

-- thread_id has the same provenance and the same gap: a writer without a
-- message usually had no thread either. Recover it from the message the call
-- now points at, again only where it is missing.
UPDATE tool_calls tc
SET thread_id  = m.thread_id,
    updated_at = now()
FROM messages m
WHERE m.id = tc.message_id
  AND tc.thread_id IS NULL
  AND m.thread_id IS NOT NULL
  AND m.thread_id <> '';

-- +goose Down

-- Not reversible, and nothing is lost by that: the prior state of these rows
-- is NULL -- the absence of a fact -- and this migration only ever fills NULLs.
SELECT 1;
