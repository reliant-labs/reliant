-- forge:backward-compatible — idx_step_executions_save_scope (the previous migration) has the same leading columns, predicate and INCLUDE list, so every lookup the previous release makes through this index is served by it.

-- Migration: drop_step_executions_saves_index
--
-- idx_step_executions_saves (workflow_id, step_id) is a prefix of
-- idx_step_executions_save_scope, so it only costs writes: every step a run
-- records maintained both.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_step_executions_saves;
