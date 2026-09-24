// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"strings"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// Temporal terminates an execution that exceeds EITHER per-execution limit
// (server defaults: limit.historyCount.error / limit.historySize.error). A
// reset forks the new run from a point INSIDE the current history, so a run
// at or near either cap cannot be rescued by resetting — see
// ErrHistoryLimitExceeded.
//
// The headroom is how close to a cap counts as "at" it. A reset gets only a
// handful of events before dying again, and routing a run to the coarse
// restart one attempt early costs nothing, while routing it to a reset one
// attempt too late costs the user another dead run.
const (
	temporalHistoryCountLimit   = 51200
	temporalHistoryHeadroom     = 500
	temporalHistorySizeLimit    = 50 * 1024 * 1024
	temporalHistorySizeHeadroom = 2 * 1024 * 1024
	historyLimitTerminateMarker = "history size exceeds limit"
	historyCountTerminateMarker = "history count exceeds limit"
)

// HistoryAtLimit reports whether a run's history is within headroom of either
// Temporal cap, from DescribeWorkflowExecution's HistoryLength and
// HistorySizeBytes. The size axis is not optional: chat 0e15fdba died on SIZE
// at ~38k events, far below the count cap, and a count-only check reset it
// straight back into its 52 MB history.
func HistoryAtLimit(historyLength, historySizeBytes int64) bool {
	return historyLength >= temporalHistoryCountLimit-temporalHistoryHeadroom ||
		historySizeBytes >= temporalHistorySizeLimit-temporalHistorySizeHeadroom
}

// IsHistoryLimitTerminateReason reports whether a WorkflowExecutionTerminated
// reason is Temporal's own history-limit kill. Observed verbatim, identity
// "history-service": "Workflow history size exceeds limit." and "Workflow
// history count exceeds limit.".
func IsHistoryLimitTerminateReason(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, historyLimitTerminateMarker) || strings.Contains(r, historyCountTerminateMarker)
}

// historyEventReader is the one Temporal call the close-event read needs.
type historyEventReader interface {
	GetWorkflowHistory(ctx context.Context, workflowID, runID string, isLongPoll bool, filterType enums.HistoryEventFilterType) client.HistoryEventIterator
}

// terminatedForHistoryLimit reads the run's close event and reports whether
// Temporal terminated it for exceeding a history limit. That is the direct
// classifier — HistoryAtLimit is a proxy that misses a run killed by the size
// cap before its describe numbers look alarming. Best-effort: a read error
// returns false and the caller's proxy checks still apply.
func terminatedForHistoryLimit(ctx context.Context, reader historyEventReader, workflowID, runID string) bool {
	iter := reader.GetWorkflowHistory(ctx, workflowID, runID, false, enums.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT)
	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return false
		}
		if attrs := event.GetWorkflowExecutionTerminatedEventAttributes(); attrs != nil {
			return IsHistoryLimitTerminateReason(attrs.GetReason())
		}
	}
	return false
}
