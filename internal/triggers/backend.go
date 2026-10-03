// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
)

// Backend is the schedule backend the API handler drives: the syncer plus the
// ability to start a manual fire.
//
// The two are one type because they are one dependency from the handler's
// point of view — "the thing that makes schedules happen" — but they need
// different Temporal surfaces (a schedule client and a workflow client), and
// bundling them here keeps that detail out of the handler.
type Backend struct {
	*Syncer
	starter   WorkflowStarter
	taskQueue string
}

// NewBackend wires the schedule client, the workflow client and the repo.
func NewBackend(schedules ScheduleClient, starter WorkflowStarter, repo Repo, taskQueue string) *Backend {
	if taskQueue == "" {
		taskQueue = defaultTaskQueue
	}
	return &Backend{
		Syncer:    NewSyncer(schedules, repo, taskQueue),
		starter:   starter,
		taskQueue: taskQueue,
	}
}

// StartManualFire runs the trigger now, returning the fire workflow id.
func (b *Backend) StartManualFire(ctx context.Context, triggerID string) (string, error) {
	return StartManualFire(ctx, b.starter, triggerID, b.taskQueue)
}
