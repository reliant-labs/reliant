-- forge:backward-compatible — CREATE OR REPLACE with the same columns, in the same order, of the same types and values; only how each value is computed changes.

-- Migration: chats_with_activity_index_driven
--
-- The chat list (ListChats), the archived list, Runs and the inbox all read
-- chats_with_activity. In prod ListChats' read of it was p50 ~184ms and p95
-- ~1.1s even with no request storm. On a scratch Postgres 16 seeded to prod
-- scale (40k chats, 160k workflows, 1.1M messages; a 2,000-chat user), the
-- all-projects list (LIMIT 300) took 780ms p50; per project (LIMIT 100) 15ms.
-- The cost was never this user's rows. It was three planner choices that
-- scale with EVERY user's rows:
--
--   1. Each EXISTS in the select list was planned as an AlternativeSubPlan,
--      and once the planner expected more than a few hundred outer rows it
--      picked the hashed alternative: build a hash of the whole table's
--      matching rows (all running workflows, all stopped ones, every pending
--      approval and question with its run) once per query. The pending
--      gates' run join became a Seq Scan of all 160k workflows, three times.
--   2. The launch event join (le.id = (SELECT ... LIMIT 1)) became a Hash
--      Join over a Seq Scan of every trigger_events row.
--   3. display_state repeated the activity CASE, with its subqueries, in
--      three WHEN branches.
--
-- So each per-chat fact is now a scalar subquery: never converted to a join
-- or a hash, so always an index probe of that one chat's rows, and, being in
-- the select list, evaluated only for the rows the query returns (after the
-- ORDER BY ... LIMIT, not before it):
--
--   * a live gate, a running workflow, a stopped workflow: SELECT true ...
--     LIMIT 1, which is NULL when there is none (a CASE WHEN NULL is false,
--     as WHEN EXISTS was);
--   * failed-after-completed: one aggregate over the chat's completed and
--     failed workflows instead of two;
--   * the running branches (5, 7, 1) test the running subquery once;
--   * the launch event's kind and trigger: one ordered probe each;
--   * display_state reads activity once, only for a running root.
--
-- idx_workflows_chat_state (the earlier migration in this change) serves
-- every workflows probe here from the index range it asks for.
--
-- Same scratch database, prepared statements, after this and with JIT off
-- (the next migration): all projects p50 34ms / p95 59ms (was 780 / 856ms);
-- per project 13 / 30ms (was 15 / 33ms); ListRuns for a 20,000-run
-- automation user 8ms (was 151ms); the archived list 39ms (was 58ms). Every
-- column of every one of the 40k chats is identical before and after
-- (EXCEPT ALL both ways, with every activity and display_state value
-- represented).
--
-- Every column keeps its name, position and type, so CREATE OR REPLACE keeps
-- the view, its column comments, and the generated projection unchanged.
CREATE OR REPLACE VIEW chats_with_activity AS
SELECT
    base.id,
    base.title,
    base.project_id,
    base.user_id,
    base.state,
    base.workflow_id,
    base.run_id,
    base.created_at,
    base.updated_at,
    base.last_active,
    base.worktree_id,
    base.workflow_name,
    base.selected_presets,
    base.archived_worktree_name,
    base.unread,
    base.active_daemon_id,
    base.adopted_at,
    base.daemon_blocked_at,
    base.no_machine,
    base.queued_for_machine_at,
    base.last_message_at,
    base.activity,
    base.root_workflow_state,
    base.root_workflow_stop_reason,
    base.launch_kind,
    base.trigger_id,
    (
        base.origin = 1
        OR base.adopted_at IS NOT NULL
    ) AS list_in_sidebar,
    (CASE
        WHEN base.root_workflow_state IS NULL OR base.root_workflow_state = 1 THEN 1
        WHEN base.root_workflow_state = 2 THEN
            CASE base.activity WHEN 2 THEN 3 WHEN 5 THEN 8 ELSE 2 END
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 3 THEN 4
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 1 THEN 5
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 2 THEN 6
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 4 THEN 7
        ELSE 0
    END)::integer AS display_state,
    base.usage_limit_resumes_at,
    base.usage_limit_provider,
    base.usage_limit_probing,
    base.working_threads,
    base.origin
FROM (
    SELECT
        c.id,
        c.title,
        c.project_id,
        c.user_id,
        c.state,
        c.workflow_id,
        c.run_id,
        c.created_at,
        c.updated_at,
        c.last_active,
        c.worktree_id,
        c.workflow_name,
        c.selected_presets,
        c.archived_worktree_name,
        c.unread,
        c.active_daemon_id,
        c.adopted_at,
        c.daemon_blocked_at,
        c.no_machine,
        c.queued_for_machine_at,
        (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) AS last_message_at,
        (CASE
            WHEN (
                SELECT true FROM pending_input_gates g
                WHERE g.chat_id = c.id AND g.live
                LIMIT 1
            ) THEN 2
            WHEN (
                SELECT true FROM workflows w
                WHERE w.chat_id = c.id AND w.state = 2
                LIMIT 1
            ) THEN
                CASE
                    WHEN c.daemon_blocked_at IS NOT NULL THEN 5
                    WHEN ul.resume_at IS NOT NULL THEN 7
                    ELSE 1
                END
            WHEN c.queued_for_machine_at IS NOT NULL THEN 6
            WHEN (
                SELECT MAX(w.completed_at) FILTER (WHERE w.stop_reason = 2)
                     > COALESCE(MAX(w.completed_at) FILTER (WHERE w.stop_reason = 1), '-infinity'::timestamptz)
                FROM workflows w
                WHERE w.chat_id = c.id AND w.state = 3 AND w.stop_reason IN (1, 2)
            ) THEN 3
            WHEN (
                SELECT true FROM workflows w
                WHERE w.chat_id = c.id AND w.state = 3 AND w.stop_reason = 3
                LIMIT 1
            ) THEN 4
            ELSE 0
        END)::integer AS activity,
        rw.state AS root_workflow_state,
        rw.stop_reason AS root_workflow_stop_reason,
        (
            SELECT te.kind FROM trigger_events te
            WHERE te.chat_id = c.id
            ORDER BY te.created_at ASC, te.id ASC
            LIMIT 1
        ) AS launch_kind,
        (
            SELECT te.trigger_id FROM trigger_events te
            WHERE te.chat_id = c.id
            ORDER BY te.created_at ASC, te.id ASC
            LIMIT 1
        ) AS trigger_id,
        ul.resume_at AS usage_limit_resumes_at,
        ul.provider AS usage_limit_provider,
        COALESCE(ul.reason = 'usage_limit_probe', false)::boolean AS usage_limit_probing,
        (
            SELECT count(DISTINCT w.thread)
            FROM workflows w
            WHERE w.chat_id = c.id
              AND w.state = 2
              AND NOT EXISTS (
                  SELECT 1 FROM pending_input_gates g
                  WHERE g.chat_id = c.id AND g.run_id = w.id
              )
              AND NOT EXISTS (
                  SELECT 1 FROM workflows ch
                  WHERE ch.parent_id = w.id AND ch.state = 2
              )
        )::integer AS working_threads,
        c.origin
    FROM chats c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN provider_backoff ul ON ul.thread_id = c.workflow_id
        AND ul.waiting_since IS NOT NULL
        AND ul.reason IN ('usage_limit', 'usage_limit_probe')
) base;
