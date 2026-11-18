// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The Temporal activities a run reports to its check through. Registered by
// the worker (workersetup), executed by the run (runtime/workflow_check.go).
const (
	StartedActivityName = "WorkflowCheckStarted"
	EndedActivityName   = "WorkflowCheckEnded"
)

// StartedInput is the WorkflowCheckStarted activity's input.
type StartedInput struct {
	ChatID string `json:"chat_id"`
}

// EndedInput is the WorkflowCheckEnded activity's input.
type EndedInput struct {
	ChatID string                   `json:"chat_id"`
	Ending core.WorkflowCheckEnding `json:"ending"`
}

// Activities are the run's two reports, over a Service.
type Activities struct{ Service Service }

// Started is the WorkflowCheckStarted activity.
func (a Activities) Started(ctx context.Context, in StartedInput) error {
	return a.Service.Started(ctx, in.ChatID)
}

// Ended is the WorkflowCheckEnded activity.
func (a Activities) Ended(ctx context.Context, in EndedInput) error {
	return a.Service.Ended(ctx, in.ChatID, in.Ending)
}
