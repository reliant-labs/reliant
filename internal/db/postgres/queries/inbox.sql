-- The Inbox: everything waiting on one user, across every chat and project
-- (research/WORKFLOW_UI.md §8). One UNION ALL so the read is one round trip;
-- every branch is scoped by c.user_id / t.user_id, never by a request value.
--
-- Dismissal is applied in Go (ListDismissedInboxItemIDs), uniformly for every
-- dismissable kind. Disabled automations are not listed: pausing is the cure.
-- kind (reliantv1.InboxItemKind): 1 approval, 2 question, 3 waiting for
-- machine, 5 automation launch failed. 4 (automation failing) is derived in Go from
-- the trigger's health — internal/triggers.ComputeHealth is its only
-- implementation — and 6 (run finished) is reserved. Archived chats are not waiting on anyone.
--
-- Generic payload columns (a_text, b_text, a_int) carry the kind-specific bits:
--   approval:        a_text title, b_text metadata JSON, a_int approval_type
--   question:        a_text thread_id, b_text metadata JSON
--   launch failed:   a_text outcome_detail, b_text event kind
--   waiting machine: a_text daemon_id, b_text daemon name

-- name: ListInboxPending :many
SELECT * FROM (
    SELECT
        1::integer AS kind,
        a.id::text AS item_key,
        c.id::text AS chat_id,
        COALESCE(c.workflow_id, c.id)::text AS run_id,
        COALESCE(c.trigger_id, '')::text AS trigger_id,
        c.project_id::text AS project_id,
        COALESCE(p.name, '')::text AS project_name,
        COALESCE(c.workflow_name, '')::text AS workflow_name,
        c.title::text AS chat_title,
        COALESCE(t.name, '')::text AS trigger_name,
        a.created_at::timestamptz AS waiting_since,
        a.title::text AS a_text,
        COALESCE(a.metadata, '')::text AS b_text,
        a.approval_type::integer AS a_int
    FROM approvals a
    JOIN chats_with_activity c ON c.id = a.chat_id
    LEFT JOIN projects p ON p.id = c.project_id
    LEFT JOIN triggers t ON t.id = c.trigger_id
    WHERE a.status = 1
      AND c.user_id = sqlc.arg('user_id')::text
      AND c.state IS DISTINCT FROM 3

    UNION ALL

    SELECT
        2,
        q.id,
        c.id,
        COALESCE(c.workflow_id, c.id),
        COALESCE(c.trigger_id, ''),
        c.project_id,
        COALESCE(p.name, ''),
        COALESCE(c.workflow_name, ''),
        c.title,
        COALESCE(t.name, ''),
        q.created_at,
        q.thread_id,
        COALESCE(q.metadata, ''),
        0
    FROM questions q
    JOIN chats_with_activity c ON c.id = q.chat_id
    LEFT JOIN projects p ON p.id = c.project_id
    LEFT JOIN triggers t ON t.id = c.trigger_id
    WHERE q.status = 1
      AND c.user_id = sqlc.arg('user_id')::text
      AND c.state IS DISTINCT FROM 3

    UNION ALL

    SELECT
        3,
        c.id,
        c.id,
        COALESCE(c.workflow_id, c.id),
        COALESCE(c.trigger_id, ''),
        c.project_id,
        COALESCE(p.name, ''),
        COALESCE(c.workflow_name, ''),
        c.title,
        COALESCE(t.name, ''),
        COALESCE(c.daemon_blocked_at, c.last_active),
        COALESCE(c.active_daemon_id, t.daemon_id, ''),
        COALESCE(d.hostname, ''),
        0
    FROM chats_with_activity c
    LEFT JOIN projects p ON p.id = c.project_id
    LEFT JOIN triggers t ON t.id = c.trigger_id
    LEFT JOIN daemons d ON d.id = COALESCE(c.active_daemon_id, t.daemon_id) AND d.user_id = c.user_id
    WHERE c.display_state = 8
      AND c.user_id = sqlc.arg('user_id')::text
      AND c.state IS DISTINCT FROM 3

    UNION ALL

    -- The newest failed launch of each trigger, unless a later firing launched.
    SELECT
        5,
        f.id,
        '',
        '',
        t.id,
        t.project_id,
        COALESCE(p.name, ''),
        t.workflow,
        '',
        t.name,
        f.occurred_at,
        f.outcome_detail,
        f.kind,
        0
    FROM triggers t
    JOIN LATERAL (
        SELECT e.id, e.kind, e.occurred_at, e.outcome_detail
        FROM trigger_events e
        WHERE e.trigger_id = t.id AND e.outcome = 'failed'
        ORDER BY e.occurred_at DESC, e.id DESC
        LIMIT 1
    ) f ON true
    LEFT JOIN projects p ON p.id = t.project_id
    WHERE t.user_id = sqlc.arg('user_id')::text
      AND t.enabled
      AND NOT EXISTS (
          SELECT 1 FROM trigger_events l
          WHERE l.trigger_id = t.id AND l.outcome = 'launched'
            AND (l.occurred_at, l.id) > (f.occurred_at, f.id)
      )
) inbox
ORDER BY kind, waiting_since, item_key;

-- name: ListDismissedInboxItemIDs :many
-- Which of the candidate item ids this user has dismissed.
SELECT item_id FROM inbox_dismissals
WHERE user_id = sqlc.arg('user_id')::text
  AND item_id = ANY(sqlc.arg('item_ids')::text[]);

-- name: DismissInboxItem :exec
INSERT INTO inbox_dismissals (user_id, item_id, dismissed_at)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, item_id) DO NOTHING;
