-- forge:backward-compatible — a new index; no statement of the previous release changes meaning.

-- Migration: tasks_plan_id_index
-- Created: 2026-11-26T06:02:58Z
--
-- Every read of a plan's tasks filters on tasks.plan_id: the planning tools'
-- ListTasksByPlan, and BranchChat's copy of the source chat's plans
-- (store.CopyChatPlansToThread). The column had no index, so each of those
-- was a full scan of tasks. Under SERIALIZABLE a full scan also takes a
-- predicate lock on the whole relation, so any task write anywhere conflicted
-- with it: BranchChat of a busy chat (one read per plan, 20 plans) failed
-- with SQLSTATE 40001 on every attempt (prod trace
-- 82a3f94880658230b94f738a9c7c066f, 2026-11-26).
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. A build that fails leaves an INVALID index that
-- IF NOT EXISTS then skips: drop it with DROP INDEX CONCURRENTLY idx_tasks_plan
-- and re-run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tasks_plan ON public.tasks (plan_id, position);
