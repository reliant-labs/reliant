// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for InboxDismissal. The generated inbox_dismissal_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: inboxDismissalRepo (a *crud.Repo[InboxDismissal]) and the
//     package-level GetInboxDismissalByPK / ListInboxDismissal / CreateInboxDismissal / UpsertInboxDismissal, keyed by
//     the whole composite key (user_id, item_id), plus UpdateInboxDismissal / DeleteInboxDismissalByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     InboxDismissal struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindInboxDismissalBy(ctx context.Context, db orm.Context, /* args */) ([]*InboxDismissal, error) {
//		var rows []*InboxDismissal
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("dismissed_at = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveInboxDismissals(ctx context.Context, db orm.Context) ([]*InboxDismissal, error) {
//		return ListInboxDismissal(ctx, db, orm.WhereEq("dismissed_at", true))
//	}
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The Inbox queries (P5b L1b): the cross-chat pending read and the per-user
// dismissals that hide items from it. The pending read lives here, beside
// the dismissals, because the inbox has no table of its own.

// The Inbox: everything waiting on one user, across every chat and project
// (research/WORKFLOW_UI.md §8). One UNION ALL so the read is one round trip;
// every branch is scoped by c.user_id / t.user_id, never by a request value.
//
// Dismissal is applied in Go (ListDismissedInboxItemIDs), uniformly for every
// dismissable kind. Disabled automations are not listed: pausing is the cure.
// kind (reliantv1.InboxItemKind): 1 approval, 2 question, 3 waiting for
// machine, 5 automation launch failed (one per failure episode), 6 run
// finished (opted-in automation). 4 (automation failing) is derived in Go from
// the trigger's firings — internal/triggers owns that rule. Archived chats are
// not waiting on anyone.
//
// Every item must be OPENABLE, so every branch INNER-joins its project and
// requires the same owner. chats.project_id has no foreign key: deleting a
// project leaves its chats (and their pending questions) behind, and the run
// page cannot open a chat whose project is gone. A LEFT JOIN here listed those
// as dead "This run doesn't exist" items. Likewise a blocking item of a run
// that completed (stop_reason 1) or was cancelled (4) can never be answered —
// nothing expires it when the run stops — so it is not listed either, and the
// same holds for the run that ASKED it (pending_input_gates names it): a
// sub-agent that completed or was cancelled leaves a question nobody can
// answer, while its root run goes on. A failed (2) or paused (3) run is
// resumable and keeps its items. An async question (asked without waiting)
// is the exception: no wait is behind it, its answer is delivered as a
// message, so it is listed whatever its runs did.
//
// Generic payload columns (a_text, b_text, a_int) carry the kind-specific bits:
//
//	approval:        a_text title, a_int approval_type
//	question:        a_text thread_id, b_text metadata JSON, a_int 1 when async
//	launch failed:   a_text newest outcome_detail, b_text event kind, a_int failures in the episode
//	waiting machine: a_text daemon_id, b_text daemon name; item_key is the chat
//	                 id and the block's start, so a later block is a new item
//
// "Its run is not over" is tested as (SELECT true ... LIMIT 1) IS NULL, not
// NOT EXISTS. The planner turns a NOT EXISTS into an anti-join and may hash
// its side: here, every user's pending gates joined to their runs (a Seq Scan
// of all workflows), which on a prod-scale database made this branch 120ms
// for a user with 2,000 chats. The scalar form is always a probe of the one
// approval's or question's gate.
//
// The approval and question branches find the caller's pending rows before
// they read those rows' chats through the view, behind an OFFSET 0 the
// planner cannot flatten. Joined directly, the planner walked every one of the
// caller's chats, computing each one's root run state, and then probed it for
// a pending approval: 200ms for an automation with 20,000 runs and two
// pending approvals, against 25ms.
//
// It is one large statement read on every inbox refresh, so it runs through
// the raw passthrough with bound $N parameters (and a plain Go slice for the
// text[] argument, which pgx encodes): bun's NewRaw would format both into
// the text, a new prepared statement per user under pgx's statement cache
// (internal/db/SCHEMA.md).
const listInboxPendingSQL = `
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
        ''::text AS b_text,
        a.approval_type::integer AS a_int
    FROM (
        -- Fenced by OFFSET 0: see listInboxPendingSQL's comment.
        SELECT a.id, a.chat_id, a.status, a.created_at, a.title, a.approval_type FROM approvals a
        JOIN chats ch ON ch.id = a.chat_id
        WHERE a.status = 1 AND ch.user_id = $1::text AND ch.state IS DISTINCT FROM 3
        OFFSET 0
    ) a
    JOIN chats_with_activity c ON c.id = a.chat_id
    JOIN projects p ON p.id = c.project_id AND p.user_id = c.user_id
    LEFT JOIN triggers t ON t.id = c.trigger_id
    WHERE a.status = 1
      AND (
          SELECT true FROM pending_input_gates g
          WHERE g.kind = 'approval' AND g.id = a.id AND g.run_state = 3 AND g.run_stop_reason IN (1, 4)
          LIMIT 1) IS NULL
      AND c.user_id = $1::text
      AND c.state IS DISTINCT FROM 3
      AND NOT (c.root_workflow_state = 3 AND c.root_workflow_stop_reason IN (1, 4))

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
        q.async::integer
    FROM (
        SELECT q.id, q.chat_id, q.status, q.created_at, q.thread_id, q.metadata, q.async FROM questions q
        JOIN chats ch ON ch.id = q.chat_id
        WHERE q.status = 1 AND ch.user_id = $1::text AND ch.state IS DISTINCT FROM 3
        OFFSET 0
    ) q
    JOIN chats_with_activity c ON c.id = q.chat_id
    JOIN projects p ON p.id = c.project_id AND p.user_id = c.user_id
    LEFT JOIN triggers t ON t.id = c.trigger_id
    WHERE q.status = 1
      AND c.user_id = $1::text
      AND c.state IS DISTINCT FROM 3
      -- An async question waits on the user whatever its run did: its answer
      -- is delivered as a message, which resumes or restarts the run. A
      -- blocking one is answerable only while its runs are.
      AND (q.async OR (
          (
              SELECT true FROM pending_input_gates g
              WHERE g.kind = 'question' AND g.id = q.id AND g.run_state = 3 AND g.run_stop_reason IN (1, 4)
              LIMIT 1) IS NULL
          AND NOT (c.root_workflow_state = 3 AND c.root_workflow_stop_reason IN (1, 4))))

    UNION ALL

    SELECT
        3,
        c.id || '@' || to_char(COALESCE(c.daemon_blocked_at, c.last_active) AT TIME ZONE 'UTC', 'YYYYMMDD"T"HH24MISS.US'),
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
    JOIN projects p ON p.id = c.project_id AND p.user_id = c.user_id
    LEFT JOIN triggers t ON t.id = c.trigger_id
    LEFT JOIN daemons d ON d.id = COALESCE(c.active_daemon_id, t.daemon_id) AND d.user_id = c.user_id
    WHERE c.display_state = 8
      -- Implied by display_state 8 (activity 5 is a blocked daemon), and a
      -- column: the user's other chats are passed over without computing
      -- their activity.
      AND c.daemon_blocked_at IS NOT NULL
      AND c.user_id = $1::text
      AND c.state IS DISTINCT FROM 3

    UNION ALL

    -- One row per failed-launch EPISODE: the failures since the trigger's last
    -- launched firing. The key is the episode's FIRST failed event, so more
    -- failures keep the same item (and the same dismissal) and a new episode,
    -- after a launch, is a new item. a_int is the episode's failure count.
    SELECT
        5,
        ep.first_id,
        '',
        '',
        t.id,
        t.project_id,
        COALESCE(p.name, ''),
        t.workflow,
        '',
        t.name,
        ep.first_at,
        f.outcome_detail,
        f.kind,
        ep.failures
    FROM triggers t
    JOIN LATERAL (
        SELECT e.id, e.kind, e.occurred_at, e.outcome_detail
        FROM trigger_events e
        WHERE e.trigger_id = t.id AND e.outcome = 'failed'
        ORDER BY e.occurred_at DESC, e.id DESC
        LIMIT 1
    ) f ON true
    LEFT JOIN LATERAL (
        SELECT l.occurred_at, l.id
        FROM trigger_events l
        WHERE l.trigger_id = t.id AND l.outcome = 'launched'
        ORDER BY l.occurred_at DESC, l.id DESC
        LIMIT 1
    ) lt ON true
    JOIN LATERAL (
        SELECT
            (array_agg(x.id ORDER BY x.occurred_at, x.id))[1]::text AS first_id,
            min(x.occurred_at)::timestamptz AS first_at,
            count(*)::integer AS failures
        FROM trigger_events x
        WHERE x.trigger_id = t.id AND x.outcome = 'failed'
          AND (lt.id IS NULL OR (x.occurred_at, x.id) > (lt.occurred_at, lt.id))
    ) ep ON true
    JOIN projects p ON p.id = t.project_id AND p.user_id = t.user_id
    WHERE t.user_id = $1::text
      AND t.enabled
      AND ep.failures > 0

    UNION ALL

    -- A completed, still-unread run of an automation that opted in to
    -- "Notify me when it finishes". Unread is the clear condition: opening the
    -- chat marks it read. A run that declared a failure outcome is a failure,
    -- not a finish. Every unattended launch kind counts — the caller passes
    -- core.UnattendedEventKinds(), the rule the unread write follows — so a
    -- webhook, provider-event or workflow-event automation is listed exactly
    -- as a schedule is.
    SELECT
        6,
        c.id,
        c.id,
        COALESCE(c.workflow_id, c.id),
        t.id,
        c.project_id,
        COALESCE(p.name, ''),
        COALESCE(c.workflow_name, ''),
        c.title,
        t.name,
        COALESCE(rw.completed_at, c.last_active),
        '',
        '',
        0
    FROM chats_with_activity c
    JOIN triggers t ON t.id = c.trigger_id AND t.user_id = c.user_id
    JOIN projects p ON p.id = c.project_id AND p.user_id = c.user_id
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    WHERE c.launch_kind = ANY($2::text[])
      AND t.notify_on_complete
      AND c.display_state = 5
      AND c.unread = 1
      AND COALESCE(rw.outcome, '') <> 'failure'
      AND c.user_id = $1::text
      AND c.state IS DISTINCT FROM 3

    UNION ALL

    -- A paused run of an automation (display_state 4: the root run is
    -- stopped with reason paused). While it is paused it holds its
    -- automation's capacity slot, so the owner is told what paused it: the
    -- newest failed step of the chat's runs. The key is the chat and when
    -- that failure happened, so a later pause is a new item.
    SELECT
        10,
        c.id || '@' || to_char(COALESCE(f.created_at, c.last_active) AT TIME ZONE 'UTC', 'YYYYMMDD"T"HH24MISS.US'),
        c.id,
        COALESCE(c.workflow_id, c.id),
        t.id,
        c.project_id,
        COALESCE(p.name, ''),
        COALESCE(c.workflow_name, ''),
        c.title,
        t.name,
        COALESCE(f.created_at, c.last_active),
        COALESCE(f.error_message, ''),
        '',
        0
    FROM chats_with_activity c
    JOIN triggers t ON t.id = c.trigger_id AND t.user_id = c.user_id
    JOIN projects p ON p.id = c.project_id AND p.user_id = c.user_id
    LEFT JOIN LATERAL (
        SELECT se.created_at, se.error_message
        FROM workflows w
        JOIN step_executions se ON se.workflow_id = w.id
        WHERE w.chat_id = c.id
          AND se.error_message IS NOT NULL
        ORDER BY se.created_at DESC
        LIMIT 1
    ) f ON true
    WHERE c.launch_kind = ANY($2::text[])
      AND c.display_state = 4
      AND c.user_id = $1::text
      AND c.state IS DISTINCT FROM 3
) inbox
ORDER BY kind, waiting_since, item_key`

// ListInboxPending returns the user's pending approvals, questions,
// waiting-for-machine runs, launch-failed automations, finished opted-in runs
// and paused automation runs, ordered by kind, then waiting time.
func ListInboxPending(ctx context.Context, db orm.Context, userID string) ([]*core.InboxPending, error) {
	// "Run finished" items follow the same rule as the unread write.
	rows, err := db.Query(ctx, listInboxPendingSQL, userID, core.UnattendedEventKinds())
	if err != nil {
		return nil, fmt.Errorf("failed to list inbox: %w", err)
	}
	defer rows.Close()
	var out []*core.InboxPending
	for rows.Next() {
		var p core.InboxPending
		if err := rows.Scan(&p.Kind, &p.ItemKey, &p.ChatID, &p.RunID, &p.TriggerID, &p.ProjectID,
			&p.ProjectName, &p.WorkflowName, &p.ChatTitle, &p.TriggerName, &p.WaitingSince,
			&p.Text1, &p.Text2, &p.Int1); err != nil {
			return nil, fmt.Errorf("failed to scan inbox item: %w", err)
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list inbox: %w", err)
	}
	return out, nil
}

// ListDismissedInboxItemIDs returns which of itemIDs the user has dismissed.
func ListDismissedInboxItemIDs(ctx context.Context, db orm.Context, userID string, itemIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(itemIDs))
	if len(itemIDs) == 0 {
		return out, nil
	}
	rows, err := ListInboxDismissal(ctx, db,
		orm.WhereEq(InboxDismissalFieldUserID, userID),
		orm.WhereIn(InboxDismissalFieldItemID, itemIDs),
		// Never more rows than candidates; past the default cap otherwise.
		orm.WithLimit(len(itemIDs)))
	if err != nil {
		return nil, fmt.Errorf("failed to list dismissed inbox items: %w", err)
	}
	for _, row := range rows {
		out[row.ItemID] = true
	}
	return out, nil
}

// dismissInboxItemsSQL dismisses every item in one statement; dismissing an
// item twice keeps the first dismissal.
const dismissInboxItemsSQL = `
INSERT INTO inbox_dismissals (user_id, item_id, dismissed_at)
SELECT ?0::text, unnest(?1::text[]), ?2::timestamptz
ON CONFLICT (user_id, item_id) DO NOTHING`

// DismissInboxItems hides itemIDs from the user's inbox. It is idempotent.
func DismissInboxItems(ctx context.Context, db orm.Context, userID string, itemIDs []string, at time.Time) error {
	if len(itemIDs) == 0 {
		return nil
	}
	if _, err := db.Bun().NewRaw(dismissInboxItemsSQL, userID, pgdialect.Array(itemIDs), at).Exec(ctx); err != nil {
		return fmt.Errorf("failed to dismiss inbox items: %w", err)
	}
	return nil
}

// RestoreInboxItems undoes DismissInboxItems; ids not dismissed are ignored.
func RestoreInboxItems(ctx context.Context, db orm.Context, userID string, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	_, err := db.Bun().NewDelete().Model((*InboxDismissal)(nil)).
		Where("user_id = ?", userID).
		Where("item_id IN (?)", bun.List(itemIDs)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to restore inbox items: %w", err)
	}
	return nil
}

// InboxDismissalExtra carries fields on InboxDismissal that are NOT columns — computed
// values, transient state, anything derived rather than stored. InboxDismissal is
// embedded on InboxDismissalExtra (see inbox_dismissal_orm.go), so a field added here is a field on
// every InboxDismissal value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type InboxDismissalExtra struct {
}
