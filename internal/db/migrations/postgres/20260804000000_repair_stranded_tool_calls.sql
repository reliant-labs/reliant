-- +goose Up

-- Repair tool calls stranded at EXECUTING by a lost terminal write.
--
-- A tool call's terminal status (completed/failed/cancelled) was written using
-- the activity's request context. That context is dead on precisely the paths
-- that produce a terminal status: user cancellation, workflow termination, and
-- activity timeout. The upsert then failed with "context canceled", the error
-- was logged and swallowed (status bookkeeping is best-effort by design, so it
-- must never fail a tool), and the row kept its EXECUTING status forever.
--
-- The UI reads that status directly, so the affected tool calls render with a
-- running spinner indefinitely -- including after a reload, because the status
-- is durable and genuinely says EXECUTING.
--
-- Measured on the dev database at the time this was written: 46 rows stuck at
-- EXECUTING, against 34 "[TOOL_STATUS] Failed to persist tool call" /
-- "context canceled" errors in a single worker log. 38 of the 46 had already
-- written their tool_result content block, which is the proof that the tool
-- itself ran to completion and only the bookkeeping was lost.
--
-- The writer is fixed in the same change as this migration (the terminal write
-- now runs on a context detached from cancellation), so this repairs history
-- rather than papering over a live defect.

-- Case 1: the result exists. The call demonstrably finished, so recover its
-- real outcome from the result block rather than guessing -- is_error
-- distinguishes a failed run from a successful one, which is exactly the
-- distinction ToolCallStatusFailed(4) vs ToolCallStatusCompleted(3) carries.
--
-- completed_at comes from the result block's timestamp: it is the closest
-- honest record of when the tool actually finished. The CHECK constraint on
-- tool_calls requires a non-null completed_at for status 3.
WITH result_blocks AS (
    SELECT DISTINCT ON (b.tool_call_id)
           b.tool_call_id,
           b.is_error,
           b.created_at
    FROM message_content_blocks b
    WHERE b.block_type = 3            -- CONTENT_BLOCK_TYPE_TOOL_RESULT
      AND b.tool_call_id IS NOT NULL
    ORDER BY b.tool_call_id, b.created_at
)
UPDATE tool_calls tc
SET status       = CASE WHEN rb.is_error THEN 4 ELSE 3 END,
    completed_at = COALESCE(tc.completed_at, rb.created_at),
    updated_at   = now()
FROM result_blocks rb
WHERE tc.id = rb.tool_call_id
  AND tc.status = 2;                  -- EXECUTING

-- Case 2: no result block exists. The call never produced output and never
-- will -- the workflow that owned it is long gone. CANCELLED(5) is the status
-- defined for "will never produce a result", and is what the backfill in
-- 20260801010000_add_tool_calls.sql already assigned to historical calls in
-- this same shape, so the two agree.
--
-- Only rows whose owning workflow is no longer running are touched: a call
-- that is genuinely executing right now must keep its status. Workflow status
-- 2 is RUNNING.
UPDATE tool_calls tc
SET status       = 5,
    completed_at = COALESCE(tc.completed_at, tc.started_at, tc.requested_at),
    updated_at   = now()
WHERE tc.status = 2
  AND NOT EXISTS (
      SELECT 1 FROM message_content_blocks b
      WHERE b.tool_call_id = tc.id AND b.block_type = 3
  )
  AND NOT EXISTS (
      SELECT 1 FROM workflows w
      WHERE w.chat_id = tc.chat_id AND w.status = 2
  );

-- +goose Down

-- Not reversible: the pre-repair state was "status is wrong", and the original
-- (incorrect) EXECUTING value carries no information worth restoring.
SELECT 1;
