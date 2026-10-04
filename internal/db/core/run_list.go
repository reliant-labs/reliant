package core

import (
	"context"
	"time"
)

// RunDisplayState is the single status a user reads for a run, folded from the
// root workflow's (state, stop_reason) and the chat's activity. The values are
// the reliantv1.RunDisplayState wire values; the SQL in queries/runs.sql
// derives them with the same table.
type RunDisplayState int32

const (
	RunDisplayUnspecified RunDisplayState = 0
	RunDisplayQueued      RunDisplayState = 1
	RunDisplayRunning     RunDisplayState = 2
	RunDisplayNeedsInput  RunDisplayState = 3
	RunDisplayPaused      RunDisplayState = 4
	RunDisplayCompleted   RunDisplayState = 5
	RunDisplayFailed      RunDisplayState = 6
	RunDisplayCancelled   RunDisplayState = 7
)

// RunCursor is a keyset position in the run list, which is ordered by
// (CreatedAt, ChatID) descending.
type RunCursor struct {
	CreatedAt time.Time
	ChatID    string
}

// RunListFilters narrows the cross-cutting run list. UserID is mandatory and
// is the only scoping: it is never taken from a request.
//
// Slices and pointers mean "unset" when empty or nil, so an unset filter never
// narrows the result.
type RunListFilters struct {
	UserID        string
	ProjectID     *string
	Workflows     []string
	TriggerID     *string
	LaunchKinds   []string
	DisplayStates []RunDisplayState
	StartedAfter  *time.Time
	StartedBefore *time.Time
	Query         *string
	After         *RunCursor
	// ByLastActive orders by most recent activity instead of start time. A
	// keyset cursor only encodes start time, so it cannot be combined with After.
	ByLastActive    bool
	IncludeArchived bool
	Limit           int
}

// RunListItem is one root run with the chat and trigger columns the Runs UI
// shows.
type RunListItem struct {
	RunID        string
	ChatID       string
	Title        string
	ProjectID    string
	WorkflowName string
	CreatedAt    time.Time
	LastActive   time.Time
	CompletedAt  *time.Time
	Outcome      string

	// RootStatus is the root workflow's lifecycle; the zero value when the chat
	// has no root workflow row yet.
	RootStatus   WorkflowStatus
	Activity     int
	DisplayState RunDisplayState

	LaunchKind  string
	TriggerID   string
	TriggerName string
	DaemonID    string
}

// RunStore reads the cross-cutting run list.
type RunStore interface {
	// ListRuns returns up to filters.Limit runs, newest first, and whether more
	// follow.
	ListRuns(ctx context.Context, filters RunListFilters) ([]*RunListItem, bool, error)
	// LastRunPerWorkflow returns each workflow name's newest run, ordered by
	// workflow name. Only UserID, ProjectID and Workflows of the filters apply.
	LastRunPerWorkflow(ctx context.Context, filters RunListFilters) ([]*RunListItem, error)
}
