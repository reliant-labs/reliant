-- +goose Up

-- Repair messages on SPAWNED threads that carry another workflow's id.
--
-- A message's workflow_id should name the workflow running its thread. The
-- timeline draws an "Agent handoff" divider when workflow_id changes on the
-- same thread, so a message carrying a foreign id makes a thread look like it
-- switched workflows mid-conversation.
--
-- SendMessage passes the workflow it is operating on. For a message addressed
-- to a spawned thread that is the PARENT's root workflow — a spawn runs inline
-- inside the parent (executeSpawnInline) and has no Temporal execution of its
-- own. So typing "continue" into a spawn stored the parent's id and drew a
-- spurious handoff above the reply.
--
-- The writer is fixed in the same change (SaveMessageToThreadWithID now
-- resolves the thread's own workflow), so this repairs history rather than
-- papering over a live defect.
--
-- ---------------------------------------------------------------------------
-- Deliberately narrow
-- ---------------------------------------------------------------------------
-- 13,374 messages have workflow_id <> thread_id, and MOST OF THEM ARE
-- CORRECT. On a FORK thread, inherited history legitimately carries the
-- workflow that produced it — that is what a fork is, and rewriting those
-- would destroy real provenance and erase handoff markers that should exist.
-- Broken down: 13,167 sit on fork threads, 0 on main.
--
-- Only these are wrong, and all four conditions must hold:
--
--   * the thread's origin is 'spawn' (not fork, not main)
--   * the message names a workflow other than its own thread
--   * a workflow row exists whose id IS the thread, in the same chat — so
--     there is a correct value to write rather than a guess
--
-- That resolves to exactly 7 rows on the measured dataset, every one a user
-- message ("continue" and similar) sent into a running spawn. The narrowness
-- is the point: an earlier repair this week matched a rule against every row
-- it could and was wrong on 116 of 117 where ground truth existed.

UPDATE messages m
SET workflow_id = m.thread_id
FROM threads t, workflows w
WHERE t.id = m.thread_id
  AND t.origin = 'spawn'
  AND w.id = m.thread_id
  AND w.chat_id = m.chat_id
  AND m.workflow_id IS NOT NULL
  AND m.workflow_id <> m.thread_id;

-- +goose Down

-- Not reversible. The prior value named a workflow that was not running this
-- thread; restoring it would restore the spurious handoff.
SELECT 1;
