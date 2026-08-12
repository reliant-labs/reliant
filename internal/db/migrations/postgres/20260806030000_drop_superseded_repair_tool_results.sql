-- +goose Up

-- Remove synthetic "interrupted" tool_result blocks that a real result later
-- superseded.
--
-- Cleanup's orphan test used to be "this tool_call has no tool_result block
-- yet". For an in-process tool that is nearly sound; for a SPAWN it is false.
-- A spawn's result is written minutes later by a separate workflow on a
-- separate thread, so at the moment cleanup runs — from
-- handleWorkflowCompletion's cancelled / panic / error paths, i.e. when a
-- worker has just crashed — every in-flight spawn looks exactly like an
-- orphan.
--
-- Reconstructed from this database: a parent workflow hit a terminal path at
-- 2026-08-04 04:54:50 while two spawn children were writing messages every few
-- seconds. Cleanup wrote "interrupted — outcome unknown" for both. Both
-- children carried on and completed SUCCESSFULLY at 05:02 and 05:05, writing
-- their genuine results. The same thing happened again on 08-05.
--
-- The result is two tool_result blocks for one tool_call_id: a fabricated
-- failure and a real success. That breaks the tool-pairing invariant, so
-- ValidateToolPairing reported `duplicate_result` and repaired the history in
-- memory on EVERY load of the affected chats — masking the corruption while
-- the fabricated failure sat in the record.
--
-- The writer is fixed in the same change as this migration (cleanup now
-- consults tool_calls.status and, for a spawn, the child workflow's status
-- before declaring anything orphaned), so this repairs history rather than
-- papering over a live defect.
--
-- Only the superseded stub is deleted, and only where a genuine result for the
-- same call exists alongside it. A call whose ONLY result is an interrupted
-- stub is left untouched: that stub may be the honest record of a tool that
-- really was interrupted, and removing it would leave an unanswered tool_use —
-- the deadlock the invariant exists to prevent.
--
-- block_type 3 = CONTENT_BLOCK_TYPE_TOOL_RESULT.
-- Measured before writing: 5 tool calls across 3 chats, every one matching the
-- shape "earlier is_error stub, later genuine result".

DELETE FROM message_content_blocks stub
WHERE stub.block_type = 3
  AND stub.tool_call_id IS NOT NULL
  AND stub.content LIKE 'Tool execution was interrupted%'
  AND EXISTS (
      SELECT 1
      FROM message_content_blocks real_result
      WHERE real_result.block_type = 3
        AND real_result.tool_call_id = stub.tool_call_id
        AND real_result.id <> stub.id
        AND real_result.content NOT LIKE 'Tool execution was interrupted%'
        -- Strictly later: the genuine result is the one that arrived after the
        -- premature repair, which is the whole signature of this bug.
        AND real_result.created_at > stub.created_at
  );

-- The repair messages themselves are left in place. Deleting a message row
-- would renumber nothing but would strand any chat_updates that reference it,
-- and an empty TOOL-role message renders as nothing. Only the false claim is
-- removed.

-- +goose Down

-- Not reversible. The deleted rows asserted an outcome ("interrupted — outcome
-- unknown") that was contradicted by the real result recorded minutes later;
-- restoring them would restore the contradiction.
SELECT 1;
