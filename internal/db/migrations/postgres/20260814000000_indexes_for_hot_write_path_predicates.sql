-- +goose Up
-- Index the predicates on the hot write/sweep paths that were reaching their
-- tables by SEQUENTIAL SCAN.
--
-- Speed is the lesser reason. Every transaction in this app runs at
-- SERIALIZABLE (BeginImmediate, internal/db/repo.go), where a sequential scan
-- takes a predicate lock over the WHOLE relation. Such a read conflicts with
-- every concurrent insert to that table -- not merely the rows it cared about
-- -- so two transactions touching completely disjoint chats abort each other
-- with SQLSTATE 40001. RunTx retries 3x, and past that the error reaches the
-- user. This is the same failure class as the MAX()+1 allocators replaced in
-- 20260813010000_sequence_counters_for_updates.sql, arriving by a different
-- route: there the predicate lock came from an aggregate over a scope, here it
-- comes from a scan that had no index to narrow it.
--
-- Measured (two SERIALIZABLE transactions, each reading its OWN chat's rows and
-- then inserting a row for that same chat -- fully disjoint row sets):
--   read served by seq scan     -> one transaction aborts, 40001
--   read served by index scan   -> both commit
-- The row sets never overlapped in either case. The scan's lock scope is the
-- entire difference.
--
-- Every index below is justified by a query measured against a copy of the
-- live dev database (221k messages, 132k tool_calls). Timings are the median of
-- 7 EXPLAIN ANALYZE runs, before -> after.

-- workflows had NO index but its primary key, while being filtered by chat_id,
-- status and parent_id on the reconciler's 30s sweep, on every chat list (the
-- chats_with_activity view runs four correlated subqueries against it per chat
-- row), and on every workflow status write. pg_stat_user_tables on the live
-- database recorded 541,191 sequential scans of this table against 229,415
-- index scans -- the most-seq-scanned table in the system by scan count. It is
-- small (1,056 rows), so each scan is individually cheap; the cost is the
-- relation-wide predicate lock each one takes, on a table written by every
-- running workflow.
CREATE INDEX IF NOT EXISTS idx_workflows_chat_id ON workflows (chat_id);
CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows (status);
CREATE INDEX IF NOT EXISTS idx_workflows_parent_id ON workflows (parent_id)
    WHERE parent_id IS NOT NULL;

-- tool_calls.thread_id and .child_workflow_id likewise had no index, and the
-- table is 132k rows, so these scans are expensive in wall-clock too. All three
-- of the reconciler's stranded-spawn sweeps run every 30 seconds against the
-- whole table:
--   ListStrandedSpawnToolCalls            14.973 ms ->  1.064 ms
--   ListStrandedBackgroundSpawnToolCalls  13.867 ms ->  1.729 ms
--   ListSpawnChildrenForThread            12.486 ms ->  0.206 ms
-- child_workflow_id is NULL on 131,979 of 132,772 rows (99.4%), so the partial
-- index covers only the ~800 spawn rows the sweeps actually join.
CREATE INDEX IF NOT EXISTS idx_tool_calls_thread_id ON tool_calls (thread_id)
    WHERE thread_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tool_calls_child_workflow_id ON tool_calls (child_workflow_id)
    WHERE child_workflow_id IS NOT NULL;

-- tool_call_results was reached only by its primary key (tool_call_id), but
-- ListToolCallResultsByMessageIDs -- on the read path of every message page --
-- filters by message_id, which meant a full scan of 130k rows returning ~130k
-- tuples to filter down to a handful.
--   ListToolCallResultsByMessageIDs (50 ids)  11.907 ms -> 0.112 ms
CREATE INDEX IF NOT EXISTS idx_tool_call_results_message_id ON tool_call_results (message_id)
    WHERE message_id IS NOT NULL;

-- chats_with_activity computes last_message_at as
--   (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id)
-- once PER CHAT ROW. With only idx_messages_chat_id that is a bitmap heap scan
-- over every message in the chat (739 rows read per chat, 2,306 buffers for a
-- 16-chat page) to compute a single maximum. Ordering the index by created_at
-- turns each subquery into a one-row index-only scan.
--   ListChats page of 50, 264 chats:  6.549 ms -> 0.969 ms (2,436 -> 179 buffers)
-- This also shrinks the view's predicate-lock footprint on `messages`, the
-- single hottest write table in the system.
CREATE INDEX IF NOT EXISTS idx_messages_chat_created_at ON messages (chat_id, created_at DESC);

-- chats is filtered by (user_id, project_id) on every chat list and had no
-- index for it. The table is small today (264 rows) so the planner's seq scan
-- is currently reasonable on cost -- but this is a per-tenant predicate on a
-- table that grows with TOTAL users, and the scan holds a relation-wide
-- predicate lock against every concurrent chat insert. Measured with 8,264
-- chats (200 simulated users x 40 chats), which is the regime this is for:
--   ListChats page of 50:  87.079 ms -> 2.272 ms
CREATE INDEX IF NOT EXISTS idx_chats_user_project ON chats (user_id, project_id);

-- step_executions is filtered by workflow_id by both of its read queries and
-- had no index for it. 1,105,045 sequential scans recorded on the live database
-- -- the highest scan COUNT of any table -- because CEL history lookups run it
-- per step. The table is tiny (25 rows), so this is entirely about not taking a
-- full-relation predicate lock a million times on a table that workflow steps
-- insert into.
CREATE INDEX IF NOT EXISTS idx_step_executions_workflow_id ON step_executions (workflow_id);

-- +goose Down
DROP INDEX IF EXISTS idx_step_executions_workflow_id;
DROP INDEX IF EXISTS idx_chats_user_project;
DROP INDEX IF EXISTS idx_messages_chat_created_at;
DROP INDEX IF EXISTS idx_tool_call_results_message_id;
DROP INDEX IF EXISTS idx_tool_calls_child_workflow_id;
DROP INDEX IF EXISTS idx_tool_calls_thread_id;
DROP INDEX IF EXISTS idx_workflows_parent_id;
DROP INDEX IF EXISTS idx_workflows_status;
DROP INDEX IF EXISTS idx_workflows_chat_id;
