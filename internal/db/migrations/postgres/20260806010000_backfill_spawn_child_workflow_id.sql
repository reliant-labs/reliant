-- +goose Up

-- Backfill tool_calls.child_workflow_id for spawn calls that predate the
-- column being written.
--
-- 20260801010000_add_tool_calls.sql declared child_workflow_id and then
-- reconstructed historical calls from message_content_blocks -- but its
-- INSERT column list does not include child_workflow_id, because the content
-- block a spawn call was rebuilt from does not record the thread the spawn
-- created. Every spawn call from before that migration therefore has a NULL
-- child_workflow_id, and the execution paths that populate it only started
-- running afterwards.
--
-- The split is exactly at the migration boundary, measured on the dev
-- database when this was written:
--
--     populated  117 rows  2026-08-01 22:55 .. 2026-08-05 20:26
--     NULL       266 rows  2026-07-09 18:31 .. 2026-08-01 22:37
--
-- child_workflow_id is what names a spawn's child thread, and the UI reads it
-- to render the spawn's transcript (a spawned sub-agent's thread id equals its
-- workflow id). With it NULL there is no thread to ask for, so every one of
-- those 266 spawns renders empty -- not wrong, but blank, permanently.
--
-- ---------------------------------------------------------------------------
-- How the child thread is recovered
-- ---------------------------------------------------------------------------
-- A spawn call's input carries the title the caller gave it, and the thread
-- the spawn created is stored with that same title. So a spawn call is matched
-- to its thread on (chat_id, origin='spawn', threads.title = input->>'title').
--
-- The obvious-looking join -- threads.parent_thread_id = tool_calls.thread_id,
-- "the child of the thread that made the call" -- is WRONG, and looks right:
-- it also matches all 266 rows. Checked against the 117 rows that already have
-- a correct child_workflow_id, it disagrees with the stored value on 116 of
-- them. A spawned thread's parent_thread_id is frequently not the thread that
-- issued the spawn. Using it would have written confidently wrong thread ids
-- into 266 rows.
--
-- The title rule below was validated the same way before being trusted:
--
--     against the 78 populated rows whose input carries a title:
--         78 agree with the stored child_workflow_id, 0 disagree
--     against the 266 NULL rows:
--         266 matched, 0 ambiguous
--
-- (The other 39 populated rows have no title in their input and cannot serve
-- as a test case; they already have their value and are not touched.)
--
-- ---------------------------------------------------------------------------
-- Guards
-- ---------------------------------------------------------------------------
-- Ambiguity is skipped, never resolved by tiebreak. A wrong thread id is worse
-- than a NULL one: NULL renders an empty preview, whereas a wrong id renders
-- ANOTHER agent's transcript inside this spawn, which no one would think to
-- distrust. So a row is repaired only when the match is unique in both
-- directions -- one candidate thread for the call, and one candidate call for
-- the thread -- and only when the thread is not already claimed by some other
-- call's child_workflow_id.
--
-- On the dev database all 266 satisfy this; the guards exist for the databases
-- this has not been run against.

WITH candidates AS (
    SELECT
        tc.id AS call_id,
        t.id  AS thread_id,
        count(*) OVER (PARTITION BY tc.id) AS threads_for_this_call,
        count(*) OVER (PARTITION BY t.id)  AS calls_for_this_thread
    FROM tool_calls tc
    JOIN threads t
      ON t.chat_id = tc.chat_id
     AND t.origin  = 'spawn'
     AND t.title   = tc.input->>'title'
    WHERE tc.tool_name = 'spawn'
      AND tc.child_workflow_id IS NULL
      AND tc.input->>'title' IS NOT NULL
      AND tc.input->>'title' <> ''
),
unambiguous AS (
    SELECT call_id, thread_id
    FROM candidates c
    WHERE threads_for_this_call = 1
      AND calls_for_this_thread = 1
      -- Not already spoken for by a call that knows its own child.
      AND NOT EXISTS (
          SELECT 1 FROM tool_calls o WHERE o.child_workflow_id = c.thread_id
      )
)
UPDATE tool_calls tc
SET child_workflow_id = u.thread_id,
    updated_at        = now()
FROM unambiguous u
WHERE tc.id = u.call_id;

-- +goose Down

-- Deliberately not reversible. The prior state of these rows is NULL -- the
-- absence of a fact, carrying no information -- and this migration only ever
-- fills NULLs, so there is nothing to restore and nothing was overwritten.
SELECT 1;
