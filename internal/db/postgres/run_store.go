package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type runStore struct {
	q pgdb.Querier
}

// NewRunStore creates the Postgres run-list store.
func NewRunStore(q pgdb.Querier) core.RunStore {
	return &runStore{q: q}
}

func (s *runStore) ListRuns(ctx context.Context, filters core.RunListFilters) ([]*core.RunListItem, bool, error) {
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

	states := make([]int32, len(filters.DisplayStates))
	for i, st := range filters.DisplayStates {
		states[i] = int32(st)
	}
	params := pgdb.ListRunsParams{
		UserID:          filters.UserID,
		IncludeArchived: filters.IncludeArchived,
		ProjectID:       chatPtrToNullString(filters.ProjectID),
		Workflows:       nonNilStrings(filters.Workflows),
		TriggerID:       chatPtrToNullString(filters.TriggerID),
		LaunchKinds:     nonNilStrings(filters.LaunchKinds),
		DisplayStates:   states,
		Query:           chatPtrToNullString(filters.Query),
		ByLastActive:    filters.ByLastActive,
		ParentChatID:    chatPtrToNullString(filters.ParentChatID),
		// One extra row tells us whether another page exists without a count.
		RowLimit: int32(limit + 1),
	}
	if filters.StartedAfter != nil {
		params.StartedAfter = sql.NullTime{Time: *filters.StartedAfter, Valid: true}
	}
	if filters.StartedBefore != nil {
		params.StartedBefore = sql.NullTime{Time: *filters.StartedBefore, Valid: true}
	}
	if filters.After != nil {
		params.CursorCreatedAt = sql.NullTime{Time: filters.After.CreatedAt, Valid: true}
		params.CursorID = sql.NullString{String: filters.After.ChatID, Valid: true}
	}

	rows, err := s.q.ListRuns(ctx, params)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list runs: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]*core.RunListItem, len(rows))
	for i, row := range rows {
		items[i] = runItem(row.ChatID, row.RunID, row.Title, row.ProjectID, row.WorkflowName,
			row.CreatedAt, row.LastActive, row.CompletedAt, row.Outcome, row.RootState, row.RootStopReason,
			row.Activity, row.DisplayState, row.LaunchKind, row.TriggerID, row.TriggerName, row.ActiveDaemonID, row.ParentChatID, row.ParentChatTitle)
	}
	return items, hasMore, nil
}

func (s *runStore) LastRunPerWorkflow(ctx context.Context, filters core.RunListFilters) ([]*core.RunListItem, error) {
	if filters.UserID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	rows, err := s.q.LastRunPerWorkflow(ctx, pgdb.LastRunPerWorkflowParams{
		UserID:    filters.UserID,
		ProjectID: chatPtrToNullString(filters.ProjectID),
		Workflows: nonNilStrings(filters.Workflows),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list last run per workflow: %w", err)
	}
	items := make([]*core.RunListItem, len(rows))
	for i, row := range rows {
		items[i] = runItem(row.ChatID, row.RunID, row.Title, row.ProjectID, row.WorkflowName,
			row.CreatedAt, row.LastActive, row.CompletedAt, row.Outcome, row.RootState, row.RootStopReason,
			row.Activity, row.DisplayState, row.LaunchKind, row.TriggerID, row.TriggerName, row.ActiveDaemonID, row.ParentChatID, row.ParentChatTitle)
	}
	return items, nil
}

// nonNilStrings keeps an unset filter a typed empty array: a nil slice binds as
// SQL NULL, and cardinality(NULL) is NULL, which would make the filter drop
// every row instead of ignoring it.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func runItem(
	chatID, runID, title, projectID, workflowName string,
	createdAt, lastActive time.Time, completedAt sql.NullTime, outcome sql.NullString,
	rootState, rootStop sql.NullInt32, activity, display int32,
	launchKind, triggerID, triggerName, daemonID sql.NullString,
	parentChatID string, parentChatTitle sql.NullString,
) *core.RunListItem {
	return &core.RunListItem{
		RunID:           runID,
		ChatID:          chatID,
		Title:           title,
		ProjectID:       projectID,
		WorkflowName:    workflowName,
		CreatedAt:       createdAt,
		LastActive:      lastActive,
		CompletedAt:     chatNullTimeToPtr(completedAt),
		Outcome:         outcome.String,
		RootStatus:      chatRootStatus(rootState, rootStop),
		Activity:        int(activity),
		DisplayState:    core.RunDisplayState(display),
		LaunchKind:      launchKind.String,
		TriggerID:       triggerID.String,
		TriggerName:     triggerName.String,
		DaemonID:        daemonID.String,
		ParentChatID:    parentChatID,
		ParentChatTitle: parentChatTitle.String,
	}
}
