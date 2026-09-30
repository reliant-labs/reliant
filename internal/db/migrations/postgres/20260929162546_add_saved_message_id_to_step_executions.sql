-- +goose NO TRANSACTION
-- +goose Up
-- Make "which message did this step save?" a scalar column instead of a fact
-- buried inside output_json.
--
-- ChatService/GetWorkflowExecutions needs exactly one field out of output_json:
-- the id of the message a `%-save` step wrote (the frontend reads it as
-- saveStep.outputJson.message_id in activityIndicators.ts). Everything else in
-- that column is dropped before the response is built. But to read one field
-- the server had to ship the whole blob out of the database, and output_json is
-- the reason this table is 905 MB against a 361 MB heap — it is TOASTed
-- out-of-line, so every row costs extra reads to detoast.
--
-- Measured on the dev database, chat abe58f03-fc55-42f3-a23a-7db19f934c9f
-- (32,023 step rows across 83 workflows, 82 MB of output_json, of which the
-- -save rows alone are 15,948 rows / 37 MB):
--
--   select scalars only, no output_json at all      ~42 ms
--   same + extract message_id from output_json     ~520 ms
--
-- So ~90% of the query's time is detoasting 37 MB to read 16k UUIDs. No index
-- fixes that, because the cost is not finding the rows — it is reading a wide
-- column the caller then throws away. The fix has to stop reading it.
--
-- GENERATED ALWAYS ... STORED rather than a plain column the writer fills:
-- the value is a pure function of two columns in the same row, so deriving it
-- in the schema makes writer and reader incapable of disagreeing. A plain
-- column would need every INSERT site to remember to populate it (today there
-- is one, registry.go:writeStepExecution, but nothing stops the second), and a
-- row where output_json says one message id and saved_message_id says another
-- is a state this version cannot represent at all.
--
-- The `IS JSON OBJECT` guard is load-bearing, not defensive dressing. A
-- generated expression is evaluated on every write, so if it can raise, it can
-- reject a legitimate INSERT: without the guard, one malformed output_json
-- would fail the activity's history write rather than just being unparsable.
-- output_json is written by json.Marshal so it is valid today (verified: 0 of
-- 201,397 `%-save` rows across the whole dev table fail `IS JSON OBJECT`), and
-- the guard keeps it true that a bad value yields NULL instead of an error.
--
-- NULL means "this step saved no message", which is the honest encoding for
-- both a non-save step and a save step whose output has no message_id.
--
-- NO TRANSACTION because this is the largest table in the database. ADD COLUMN
-- with a STORED generated column DOES rewrite the table (unlike a plain
-- nullable column), and holding that rewrite plus the index build in a single
-- transaction would keep ACCESS EXCLUSIVE for the whole duration — every read
-- and write of step_executions blocks behind it, which is every running
-- workflow. Split up, the rewrite commits before the index build begins, and
-- the index builds CONCURRENTLY without blocking writers. Same reasoning as
-- 20260926000000_add_workflow_owner_user_id.sql.
ALTER TABLE step_executions
    ADD COLUMN IF NOT EXISTS saved_message_id text
    GENERATED ALWAYS AS (
        CASE
            WHEN step_id LIKE '%-save' AND output_json IS JSON OBJECT
            THEN output_json::jsonb ->> 'message_id'
        END
    ) STORED;

-- The read path is "every step of every workflow of one chat, in execution
-- order": workflows(chat_id) -> step_executions(workflow_id), sorted by
-- (workflow_id, created_at). Leading with workflow_id keeps this index a
-- superset of the single-column idx_step_executions_workflow_id it replaces,
-- so the per-workflow lookups elsewhere (GetAllStepExecutionsForWorkflow,
-- DeleteStepExecutionsForWorkflow) are served by it too.
--
-- created_at as the second key supplies the ORDER BY, which is what turns the
-- plan's sort into an incremental sort over already-ordered groups instead of
-- an external merge to disk (measured before: "Sort Method: external merge
-- Disk: 10536kB" across three workers).
--
-- INCLUDE carries the scalar payload so the hot path can answer from the index
-- alone and never touch the heap tuples — which is the point, because those
-- heap tuples are the wide ones with the TOAST pointers. saved_message_id is
-- in here for exactly that reason: it is the one value we still need per row.
--
-- Dropped-then-created for the same reason as the workflows migration: a failed
-- CONCURRENTLY build leaves an INVALID index that IF NOT EXISTS would accept as
-- finished, producing an index the planner silently never uses.
DROP INDEX CONCURRENTLY IF EXISTS idx_step_executions_chat_read;

CREATE INDEX CONCURRENTLY idx_step_executions_chat_read
    ON step_executions (workflow_id, created_at)
    INCLUDE (id, step_id, activity_name, exit_code, success, duration_ms,
             loop_node_id, loop_iteration, saved_message_id);

-- Now redundant: the new index has workflow_id as its leading column, so every
-- lookup that used this one is served by it. Keeping both would cost a second
-- index maintenance write on the hottest insert path in the engine.
DROP INDEX CONCURRENTLY IF EXISTS idx_step_executions_workflow_id;
