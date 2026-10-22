// Copyright (c) 2025 Reliant Labs
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

// The cross-cutting run list (RunService.ListRuns, the list_runs tool), the
// forge-ORM port of queries/runs.sql (P5b lane L2). A run has no table of its
// own: it is a chat row (chat id == root workflow id), so the list is
// chats_with_activity joined to its root workflow and, for automation-fired
// runs, to the trigger. display_state is the one status vocabulary the UI
// shows, derived once in chats_with_activity.display_state so a filter on it
// and the label a row carries cannot disagree:
//
//	1 queued       no root workflow row yet, or PENDING
//	2 running      ACTIVE
//	3 needs-input  ACTIVE and activity = awaiting input (2)
//	8 waiting-for-machine  ACTIVE and activity = waiting for daemon (5)
//	4 paused       STOPPED, stop_reason PAUSED (3)
//	5 completed    STOPPED, stop_reason COMPLETED (1)
//	6 failed       STOPPED, stop_reason FAILED (2)
//	7 cancelled    STOPPED, stop_reason CANCELLED (4)
//
// The row set is one hand-written select (runRowsSQL); the filters a caller
// sets are composed onto it with the bun builder, so an unset filter adds no
// predicate at all.

// runRowsSQL is one row per run of user ?0, as the r of the list queries.
// The parent chat of an agent-started run lives only in its launch event's
// payload, looked up for agent.start_run chats alone, by chat id
// (idx_trigger_events_chat), so every other row pays nothing. The parent's
// title is owner-scoped: a parent that is not the caller's contributes none.
const runRowsSQL = `
    SELECT
        c.id AS chat_id,
        COALESCE(c.workflow_id, c.id)::text AS run_id,
        c.title,
        c.project_id,
        c.created_at,
        c.last_active,
        rw.completed_at AS completed_at,
        COALESCE(rw.workflow_name, c.workflow_name, '')::text AS workflow_name,
        rw.outcome AS outcome,
        rw.state AS root_state,
        rw.stop_reason AS root_stop_reason,
        c.activity,
        c.launch_kind,
        c.trigger_id,
        t.name AS trigger_name,
        c.active_daemon_id,
        COALESCE(c.display_state, 1)::integer AS display_state,
        c.state AS chat_state,
        COALESCE(pe.parent_chat_id, '')::text AS parent_chat_id,
        pc.title AS parent_chat_title
    FROM chats_with_activity c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN triggers t ON t.id = c.trigger_id AND t.user_id = c.user_id
    LEFT JOIN LATERAL (
        SELECT te.payload->>'parent_chat_id' AS parent_chat_id
        FROM trigger_events te
        WHERE te.chat_id = c.id AND c.launch_kind = 'agent.start_run'
        ORDER BY te.created_at ASC, te.id ASC
        LIMIT 1
    ) pe ON true
    LEFT JOIN chats pc ON pc.id = pe.parent_chat_id AND pc.user_id = c.user_id
    WHERE c.user_id = ?0`

// runChildOfSQL narrows runRowsSQL to the runs an agent started from chat
// ?1: the reverse direction of the parent lookup, driven from
// idx_trigger_events_parent_chat.
const runChildOfSQL = `
      AND c.id IN (
            SELECT te.chat_id FROM trigger_events te
            WHERE te.kind = 'agent.start_run'
              AND te.payload->>'parent_chat_id' = ?1::text
              AND te.chat_id IS NOT NULL)`

var runRowColumns = columnsOf[runRow]("r")

// selectRuns selects runRow's columns over the runs of userID, narrowed to
// the children of parentChatID when it is set.
func selectRuns(db orm.Context, userID string, parentChatID *string, distinctOn string) *bun.SelectQuery {
	rowsSQL, args := runRowsSQL, []any{userID}
	if parentChatID != nil {
		rowsSQL += runChildOfSQL
		args = append(args, *parentChatID)
	}
	q := db.Bun().NewSelect().TableExpr("("+rowsSQL+") AS r", args...)
	if distinctOn != "" {
		q = q.DistinctOn(distinctOn)
	}
	return q.ColumnExpr(runRowColumns)
}

// whereProjectAndWorkflows applies the two filters both list queries share.
func whereProjectAndWorkflows(q *bun.SelectQuery, filters core.RunListFilters) *bun.SelectQuery {
	if filters.ProjectID != nil {
		q = q.Where("r.project_id = ?", *filters.ProjectID)
	}
	if len(filters.Workflows) > 0 {
		q = q.Where("r.workflow_name = ANY(?)", pgdialect.Array(filters.Workflows))
	}
	return q
}

// ListRuns returns up to filters.Limit runs, newest first (or most recently
// active first, with ByLastActive), and whether more follow.
func ListRuns(ctx context.Context, db orm.Context, filters core.RunListFilters) ([]*core.RunListItem, bool, error) {
	if filters.UserID == "" {
		return nil, false, fmt.Errorf("user ID cannot be empty")
	}
	limit := filters.Limit
	if limit <= 0 {
		return nil, false, fmt.Errorf("limit must be positive")
	}
	if filters.ByLastActive && filters.After != nil {
		return nil, false, fmt.Errorf("a page cursor cannot be combined with last-active ordering")
	}

	q := selectRuns(db, filters.UserID, filters.ParentChatID, "")
	if !filters.IncludeArchived {
		q = q.Where("r.chat_state IS DISTINCT FROM ?", core.ChatStateArchived)
	}
	q = whereProjectAndWorkflows(q, filters)
	if filters.TriggerID != nil {
		q = q.Where("r.trigger_id = ?", *filters.TriggerID)
	}
	// A chat with no launch event is an interactive start, as ListChats
	// treats it. With no kind named, builder test runs are left out: they are
	// scratch runs from the workflow builder, not the user's real history.
	// Asking for 'builder.test' explicitly (the Runs "Tests" filter) brings
	// them back.
	if len(filters.LaunchKinds) == 0 {
		q = q.Where("COALESCE(r.launch_kind, 'chat.start') <> 'builder.test'")
	} else {
		q = q.Where("COALESCE(r.launch_kind, 'chat.start') = ANY(?)", pgdialect.Array(filters.LaunchKinds))
	}
	if len(filters.DisplayStates) > 0 {
		states := make([]int32, len(filters.DisplayStates))
		for i, st := range filters.DisplayStates {
			states[i] = int32(st)
		}
		q = q.Where("r.display_state = ANY(?)", pgdialect.Array(states))
	}
	if filters.StartedAfter != nil {
		q = q.Where("r.created_at >= ?", *filters.StartedAfter)
	}
	if filters.StartedBefore != nil {
		q = q.Where("r.created_at < ?", *filters.StartedBefore)
	}
	if filters.Query != nil {
		q = q.Where("position(lower(?) in lower(r.title)) > 0", *filters.Query)
	}
	if filters.After != nil {
		q = q.Where("(r.created_at, r.chat_id) < (?, ?)", filters.After.CreatedAt, filters.After.ChatID)
	}
	if filters.ByLastActive {
		q = q.OrderExpr("r.last_active DESC NULLS LAST")
	}
	// One extra row tells us whether another page exists without a count.
	q = q.OrderExpr("r.created_at DESC, r.chat_id DESC").Limit(int64(limit + 1))

	var rows []runRow
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, false, fmt.Errorf("failed to list runs: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return runItems(rows), hasMore, nil
}

// LastRunPerWorkflow returns each workflow name's newest run, ordered by
// workflow name. Only UserID, ProjectID and Workflows of the filters apply.
// Archived runs count: an automation archives each run it finishes
// successfully, so leaving them out would make a healthy automation's
// workflow read as never having run.
func LastRunPerWorkflow(ctx context.Context, db orm.Context, filters core.RunListFilters) ([]*core.RunListItem, error) {
	if filters.UserID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	q := selectRuns(db, filters.UserID, nil, "r.workflow_name").
		Where("r.launch_kind IS DISTINCT FROM 'builder.test'").
		Where("r.workflow_name <> ''")
	q = whereProjectAndWorkflows(q, filters).
		OrderExpr("r.workflow_name, r.created_at DESC, r.chat_id DESC")

	var rows []runRow
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list last run per workflow: %w", err)
	}
	return runItems(rows), nil
}

// runRow is one row of runRowsSQL.
type runRow struct {
	ChatID          string                   `bun:"chat_id"`
	RunID           string                   `bun:"run_id"`
	Title           string                   `bun:"title"`
	ProjectID       string                   `bun:"project_id"`
	CreatedAt       time.Time                `bun:"created_at"`
	LastActive      time.Time                `bun:"last_active"`
	CompletedAt     *time.Time               `bun:"completed_at"`
	WorkflowName    string                   `bun:"workflow_name"`
	Outcome         *string                  `bun:"outcome"`
	RootState       *core.WorkflowState      `bun:"root_state"`
	RootStopReason  *core.WorkflowStopReason `bun:"root_stop_reason"`
	Activity        int32                    `bun:"activity"`
	LaunchKind      *string                  `bun:"launch_kind"`
	TriggerID       *string                  `bun:"trigger_id"`
	TriggerName     *string                  `bun:"trigger_name"`
	ActiveDaemonID  *string                  `bun:"active_daemon_id"`
	DisplayState    int32                    `bun:"display_state"`
	ChatState       *core.ChatState          `bun:"chat_state"`
	ParentChatID    string                   `bun:"parent_chat_id"`
	ParentChatTitle *string                  `bun:"parent_chat_title"`
}

func runItems(rows []runRow) []*core.RunListItem {
	items := make([]*core.RunListItem, len(rows))
	for i, row := range rows {
		item := &core.RunListItem{
			RunID:           row.RunID,
			ChatID:          row.ChatID,
			Title:           row.Title,
			ProjectID:       row.ProjectID,
			WorkflowName:    row.WorkflowName,
			CreatedAt:       row.CreatedAt,
			LastActive:      row.LastActive,
			CompletedAt:     row.CompletedAt,
			Outcome:         deref(row.Outcome),
			Activity:        int(row.Activity),
			DisplayState:    core.RunDisplayState(row.DisplayState),
			LaunchKind:      deref(row.LaunchKind),
			TriggerID:       deref(row.TriggerID),
			TriggerName:     deref(row.TriggerName),
			DaemonID:        deref(row.ActiveDaemonID),
			ParentChatID:    row.ParentChatID,
			ParentChatTitle: deref(row.ParentChatTitle),
			Archived:        row.ChatState != nil && *row.ChatState == core.ChatStateArchived,
		}
		// The zero status when the chat has no root workflow row yet: the
		// join leaves both halves NULL.
		if row.RootState != nil {
			item.RootStatus.State = *row.RootState
			if row.RootStopReason != nil {
				item.RootStatus.StopReason = *row.RootStopReason
			}
		}
		items[i] = item
	}
	return items
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
