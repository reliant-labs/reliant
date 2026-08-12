-- +goose Up

-- Scope historical error events to the thread that produced them.
--
-- An error update carrying no `thread` is chat-global, and the timeline has
-- nothing to filter it by — so ONE "Paused: no machine is connected" from the
-- main thread renders inside EVERY thread of the chat, including spawns that
-- started hours later and never saw the outage.
--
-- The writer was fixed first (WorkflowErrorInput.Thread, threaded through
-- notifyWorkflowError and the hand-built retry_exhaustion payloads), so errors
-- emitted since carry a thread. The renderer filters on it. What remained is
-- the data: 118 error events predating that change, which the renderer
-- deliberately keeps visible everywhere rather than guessing a thread for —
-- and which therefore still appear in every spawn.
--
-- ---------------------------------------------------------------------------
-- Why workflow_id is a sound key
-- ---------------------------------------------------------------------------
-- These payloads already carry the workflow that raised the error, and
-- workflows.thread records that workflow's thread. So this is a lookup along
-- an existing relation, not an inference about which thread "probably" owned
-- the error.
--
-- Validated before writing, on the dev database:
--
--   * all 118 unscoped errors carry a non-empty workflow_id
--   * all 118 resolve to a workflows row, and every one of those rows belongs
--     to the SAME chat as the error (0 cross-chat matches)
--   * every resolved thread exists in `threads` and belongs to that same chat
--   * against the 5 errors that already carry a thread (written by the fixed
--     code), the rule reproduces the stored value 5/5 with 0 disagreements
--
-- That last check is the one that matters. An earlier backfill this week used
-- a rule that looked obviously right (threads.parent_thread_id = the calling
-- thread), matched every row, and disagreed with the stored answer on 116 of
-- 117 known-good rows. A rule is only trustworthy where it reproduces facts
-- already recorded.
--
-- Rows whose workflow cannot be resolved are left unscoped: they stay visible
-- everywhere, which is the honest fallback and the current behaviour.

UPDATE chat_updates cu
SET data = jsonb_set(cu.data::jsonb, '{thread}', to_jsonb(w.thread), true)::text
FROM workflows w
WHERE w.id = cu.data::jsonb->>'workflow_id'
  AND cu.data::jsonb->>'update_type' = 'error'
  AND NOT (cu.data::jsonb ? 'thread')
  AND w.thread IS NOT NULL
  AND w.thread <> ''
  -- Never attribute an error to a thread in a different conversation. This is
  -- 0 rows on the measured dataset; it is here so the statement cannot do
  -- damage on a database where that does not hold.
  AND w.chat_id = cu.chat_id;

-- +goose Down

-- Not reversible, and nothing is lost: the prior state is the ABSENCE of a
-- thread — no information — and this only fills that absence with a value
-- read from the error's own workflow.
SELECT 1;
