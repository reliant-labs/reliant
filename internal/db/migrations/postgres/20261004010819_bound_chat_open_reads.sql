-- +goose NO TRANSACTION
-- +goose Up
-- Indexes for opening a long chat by reading what the page shows, not the
-- chat's whole history.
--
-- Opening chat 8bb0a875 (48k messages, 199 workflows, 93k step rows, 266k
-- chat_updates) took >10s, and the 200 messages it rendered were never the
-- slow part. Two side reads scaled with the chat's entire history:
--
--   GetWorkflowExecutions   every step row of every workflow: 93,568 rows,
--                           56.8 MB of JSON, ~1s of SQL (parallel seq scan of
--                           step_executions + external sort to disk). The
--                           timeline renders 6 of those rows.
--   chat snapshot           DISTINCT ON over all 266k chat_updates of the chat
--                           (external sort, ~3-5s) to find the latest update
--                           per entity — 25,660 of whose 27,279 results were
--                           historical tool-call statuses the message window
--                           already carries on its own blocks.
--
-- Each index below serves one of the bounded reads that replace those. Sizes
-- and timings are from a copy of the dev database (544k step rows, 2.48M
-- chat_updates).
--
-- Every index is dropped-then-created CONCURRENTLY: a failed CONCURRENTLY build
-- leaves an INVALID index that IF NOT EXISTS would accept as finished, and the
-- planner never uses an invalid index — a silent fall back to the slow plan.

-- ── Timeline steps (GetWorkflowExecutions, BASIC view) ──────────────────────
--
-- The chat timeline needs only steps whose activity is user-facing — the ones
-- it draws an activity indicator for. Everything else is internal plumbing
-- (SaveMessage / CallLLM / ExecuteTools / ...), which is 99.9% of the rows.
-- A partial index over the user-facing remainder is 96 kB against the 114 MB
-- full one, and turns the read into an index probe per workflow: ~6ms against
-- ~1s.
--
-- The predicate MUST stay in step with workflowmodel.InternalActivities, and
-- the query that uses this index repeats it literally rather than taking it as
-- a parameter: the planner can only prove a query is covered by a partial
-- index from a predicate it can see at plan time, and a bound array is not
-- one. TestUserFacingStepIndexPredicateMatchesInternalActivities pins the two
-- together, so adding an internal activity without updating this index fails
-- a test instead of quietly resurrecting the seq scan.
DROP INDEX CONCURRENTLY IF EXISTS idx_step_executions_user_facing;

CREATE INDEX CONCURRENTLY idx_step_executions_user_facing
    ON step_executions (workflow_id, created_at)
    INCLUDE (id, step_id, activity_name, exit_code, success, duration_ms,
             loop_node_id, loop_iteration)
    WHERE activity_name NOT IN (
        'WorkflowStatus', 'WorkflowError', 'Cleanup', 'FetchThreadResult',
        'FailStep', 'SaveMessage', 'CallLLM', 'Approval', 'ExecuteTools');

-- The timeline also needs, for each user-facing step, whether its "-save"
-- sibling recorded a message (then the step renders AS that message, not as an
-- indicator). Those siblings are found by (workflow_id, step_id) among steps
-- that saved one — which this index answers directly, instead of a scan of
-- every save row of the chat.
DROP INDEX CONCURRENTLY IF EXISTS idx_step_executions_saves;

CREATE INDEX CONCURRENTLY idx_step_executions_saves
    ON step_executions (workflow_id, step_id)
    INCLUDE (loop_node_id, loop_iteration, saved_message_id, id, created_at)
    WHERE saved_message_id IS NOT NULL;

-- ── Chat snapshot: latest update per entity ─────────────────────────────────
--
-- The snapshot replays, for each entity, its newest chat_update. Read as
-- DISTINCT ON over the chat's whole history that sorts every row (266k for the
-- worst chat). Read as a skip scan — one index probe per DISTINCT entity — it
-- touches ~1.3k rows. That needs an index ordered (chat_id, entity_id,
-- sequence_number DESC) covering exactly the rows the snapshot considers.
--
-- Excluded types are the ones the snapshot never takes from this table:
-- messages (1) and stream_finalized markers (19) as before, and now tool calls
-- (4), whose status comes from the durable tool_calls row for just the calls
-- the client can see (see buildChatSnapshot). Together those are 85% of the
-- table, which is why this is 122 MB where the index it replaces is 438 MB.
DROP INDEX CONCURRENTLY IF EXISTS idx_chat_updates_snapshot_heads;

CREATE INDEX CONCURRENTLY idx_chat_updates_snapshot_heads
    ON chat_updates (chat_id, entity_id, sequence_number DESC)
    WHERE update_type NOT IN (1, 4, 19);

-- Replaced by the partial index above. It was added for this same query, but
-- the query's computed dedup key meant the planner never used it: 0 scans on
-- the dev database, 438 MB, maintained on every insert into the hottest
-- table in the system.
DROP INDEX CONCURRENTLY IF EXISTS idx_chat_updates_chat_entity_seq;

-- ── Chat snapshot: tool calls still in flight ───────────────────────────────
--
-- A still-running tool call's card needs its live status even when the
-- message holding it is outside the snapshot's window. Calls that are pending,
-- executing or backgrounded (1, 2, 6) are a few dozen rows in the whole
-- database; a partial index on them is 56 kB and makes "this chat's in-flight
-- calls" a single probe instead of a scan of the chat's 25k terminal ones.
DROP INDEX CONCURRENTLY IF EXISTS idx_tool_calls_chat_live;

CREATE INDEX CONCURRENTLY idx_tool_calls_chat_live
    ON tool_calls (chat_id)
    WHERE status IN (1, 2, 6);
