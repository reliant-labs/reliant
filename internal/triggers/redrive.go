// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// redriveAfter is how long an inbound event may stay pending before its
	// fire is restarted. Longer than a healthy fire takes to claim it, and
	// harmless if it is not: restarting a running fire attaches to it.
	redriveAfter = 2 * time.Minute
	// redriveGiveUp closes out an event that has been pending this long. A
	// run started a day after its event is not what the owner asked for.
	redriveGiveUp = 24 * time.Hour
	// redriveBatch bounds one pass.
	redriveBatch = 200
	// redriveInterval is the pass period.
	redriveInterval = time.Minute
)

// Redriver restarts the fires of inbound events left pending: the receiver
// wrote the row and crashed before the start, Temporal refused the start,
// or the fire exhausted its retries on a transient failure.
//
// It is what makes "a sender that never retries still gets exactly one run"
// true. GitHub drops a delivery that is not acked in 10 seconds and never
// redelivers it on its own, so the receiver acks as soon as the row is
// written and this closes the gap between the row and the run.
type Redriver struct {
	repo      EventRepo
	starter   WorkflowStarter
	taskQueue string
	now       func() time.Time
}

// NewRedriver builds a Redriver. taskQueue empty means the shared queue.
func NewRedriver(repo EventRepo, starter WorkflowStarter, taskQueue string) *Redriver {
	if taskQueue == "" {
		taskQueue = defaultTaskQueue
	}
	return &Redriver{repo: repo, starter: starter, taskQueue: taskQueue, now: time.Now}
}

// RedriveOnce runs one pass. Every replica may run it concurrently: a start
// names the event's fire id, so racing starts collapse into one execution.
func (r *Redriver) RedriveOnce(ctx context.Context) error {
	now := r.now()
	stale, err := r.repo.ListStalePendingTriggerEvents(ctx, now.Add(-redriveAfter), redriveBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, ev := range stale {
		if ev.TriggerID == nil {
			if _, err := r.repo.SettlePendingTriggerEvent(ctx, ev.ID, core.TriggerEventSkipped, "trigger deleted"); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if ev.CreatedAt.Before(now.Add(-redriveGiveUp)) {
			if _, err := r.repo.SettlePendingTriggerEvent(ctx, ev.ID, core.TriggerEventFailed,
				"the event was never launched; it is too old to start now"); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		input := EventFireInput{
			TriggerID: *ev.TriggerID,
			Kind:      ev.Kind,
			DedupeKey: ev.DedupeKey,
			Manual:    ev.Payload[payloadManual] == true,
		}
		if err := StartEventFire(ctx, r.starter, ev.ID, input, r.taskQueue); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run redrives on an interval until ctx ends. It never blocks startup and
// never exits on an error: a pass that fails is retried by the next one.
func (r *Redriver) Run(ctx context.Context) {
	ticker := time.NewTicker(redriveInterval)
	defer ticker.Stop()
	for {
		if err := r.RedriveOnce(ctx); err != nil && ctx.Err() == nil {
			logging.Warn("trigger event redrive pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
