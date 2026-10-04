// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// schedulePrefix namespaces our schedule ids inside the Temporal namespace, so
// SyncAll can tell a schedule it owns from one some other component created.
//
// It deliberately carries no per-deployment discriminator. Two deployments
// sharing one Temporal namespace would delete each other's schedules as
// orphans, but that topology does not exist: every deployment gets its own
// namespace — dev derives one per stack (control-plane
// deploy/kcl/lib/identity.k temporal_namespace, passed as TEMPORAL_NAMESPACE),
// and e2e/prod each run their own in-cluster Temporal. The namespace IS the
// discriminator. Pointing two deployments (two databases) at one namespace
// is unsupported; if that is ever wanted, add the discriminator here AND to
// FireWorkflowID, and migrate existing schedules.
const schedulePrefix = "trigger-"

// ScheduleID is the Temporal Schedule id for a trigger.
func ScheduleID(triggerID string) string { return schedulePrefix + triggerID }

// FireWorkflowID is the scheduled fire's workflow id. Temporal appends the
// scheduled time, which makes each fire's id unique — and that id is what the
// event row dedupes on, so a retried fire cannot launch twice.
func FireWorkflowID(triggerID string) string { return schedulePrefix + "fire-" + triggerID }

// ManualFireWorkflowID is the workflow id for a "run now". The uuid keeps
// repeated manual fires from colliding with each other or with a scheduled
// fire, and it is also the dedupe key, so each manual fire launches once.
func ManualFireWorkflowID(triggerID string) string {
	return FireWorkflowID(triggerID) + "-manual-" + uuid.NewString()
}

// Syncer converges Temporal Schedules onto the triggers table.
type Syncer struct {
	schedules ScheduleClient
	repo      Repo
	taskQueue string
}

// NewSyncer builds a Syncer. taskQueue is the queue the fire workflow runs on;
// empty means the shared queue.
func NewSyncer(schedules ScheduleClient, repo Repo, taskQueue string) *Syncer {
	if taskQueue == "" {
		taskQueue = defaultTaskQueue
	}
	return &Syncer{schedules: schedules, repo: repo, taskQueue: taskQueue}
}

// Sync makes the Temporal Schedule for t match the row: creating it when it is
// absent, replacing its spec and policies when it drifted, and paused exactly
// when the trigger is disabled.
//
// It is idempotent, which is what lets every write path call it unconditionally
// and lets SyncAll repair drift at startup without knowing what drifted.
//
// It converges from a FRESH read of the row, never from the caller's copy:
// two concurrent writes each call Sync after their own commit, and whichever
// Temporal call lands last must carry the latest row, not the one its caller
// happened to hold. A row that is gone converges to no schedule.
func (s *Syncer) Sync(ctx context.Context, triggerID string) error {
	t, err := s.repo.GetTrigger(ctx, triggerID)
	if err != nil {
		if errors.Is(err, core.ErrTriggerNotFound) {
			return s.Delete(ctx, triggerID)
		}
		return fmt.Errorf("load trigger %s: %w", triggerID, err)
	}
	return s.converge(ctx, t)
}

// converge makes the schedule match t, which the caller has just read.
func (s *Syncer) converge(ctx context.Context, t *core.Trigger) error {
	if t == nil {
		return errors.New("triggers: converge called with nil trigger")
	}
	if t.Kind != core.TriggerKindSchedule {
		return fmt.Errorf("triggers: cannot sync a schedule for kind %q", t.Kind)
	}

	sched, err := ScheduleFor(t)
	if err != nil {
		return err
	}

	desired := s.scheduleBody(t, sched)
	paused := !t.Enabled

	handle := s.schedules.GetHandle(ctx, ScheduleID(t.ID))
	if _, err := handle.Describe(ctx); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("describe schedule %s: %w", ScheduleID(t.ID), err)
		}
		_, err = s.schedules.Create(ctx, client.ScheduleOptions{
			ID:            ScheduleID(t.ID),
			Spec:          sched.Spec,
			Action:        desired.Action,
			Overlap:       sched.TemporalOverlap(),
			CatchupWindow: sched.CatchupWindow,
			Paused:        paused,
			Note:          scheduleNote(t),
		})
		if err != nil && !isAlreadyExists(err) {
			return fmt.Errorf("create schedule %s: %w", ScheduleID(t.ID), err)
		}
		if err == nil {
			return nil
		}
		// Lost a race with a concurrent Sync; fall through and update.
	}

	// Replace the whole body rather than diffing it. The row is the truth, so
	// "what changed" is not a question worth asking — and a partial update is
	// how a schedule ends up half converged.
	err = handle.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			body := desired
			state := &client.ScheduleState{Paused: paused, Note: scheduleNote(t)}
			if in.Description.Schedule.State != nil {
				// Preserve the action budget; only pause state and the spec
				// are ours to own.
				state.LimitedActions = in.Description.Schedule.State.LimitedActions
				state.RemainingActions = in.Description.Schedule.State.RemainingActions
			}
			body.State = state
			return &client.ScheduleUpdate{Schedule: &body}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("update schedule %s: %w", ScheduleID(t.ID), err)
	}
	return nil
}

// Delete removes the Temporal Schedule for a trigger. A schedule that is
// already gone is success: the caller's intent is "no schedule for this id",
// and reporting an error for the state they asked for would make the delete
// path non-retryable for no reason.
func (s *Syncer) Delete(ctx context.Context, triggerID string) error {
	if err := s.schedules.GetHandle(ctx, ScheduleID(triggerID)).Delete(ctx); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete schedule %s: %w", ScheduleID(triggerID), err)
	}
	return nil
}

// NextFireAt returns the next time the trigger's schedule will fire, or nil
// when it is paused or has no upcoming time.
func (s *Syncer) NextFireAt(ctx context.Context, triggerID string) (*time.Time, error) {
	desc, err := s.schedules.GetHandle(ctx, ScheduleID(triggerID)).Describe(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if desc.Schedule.State != nil && desc.Schedule.State.Paused {
		return nil, nil
	}
	if len(desc.Info.NextActionTimes) == 0 {
		return nil, nil
	}
	next := desc.Info.NextActionTimes[0]
	return &next, nil
}

// SyncAll converges every trigger, then deletes orphaned schedules — ones
// whose row is gone. Run at api-server startup: a delete that failed, or a row
// written while Temporal was unreachable, leaves exactly this kind of drift,
// and an orphan schedule keeps firing for a trigger nobody can see or stop.
//
// Orphan cleanup is BEST EFFORT on any single call, because the schedule List
// it depends on is backed by visibility and is eventually consistent — a
// schedule created moments ago may not appear yet. That is why this is
// idempotent and runs at every startup rather than once: the converge half is
// immediate, and a missed orphan is collected by the next pass.
//
// The converge half never deletes, so a stale List cannot remove a live
// trigger's schedule: ids absent from the listing are simply not considered.
// The orphan half re-reads the row before each delete, so a trigger created
// after the snapshot keeps its schedule.
func (s *Syncer) SyncAll(ctx context.Context) error {
	all, err := s.repo.ListAllTriggers(ctx)
	if err != nil {
		return fmt.Errorf("list triggers: %w", err)
	}

	live := make(map[string]struct{}, len(all))
	var errs []error
	for _, t := range all {
		if t.Kind != core.TriggerKindSchedule {
			continue
		}
		live[ScheduleID(t.ID)] = struct{}{}
		if err := s.converge(ctx, t); err != nil {
			// Keep going: one malformed trigger must not stop the rest of the
			// fleet from being repaired.
			errs = append(errs, fmt.Errorf("sync trigger %s: %w", t.ID, err))
		}
	}

	iter, err := s.schedules.List(ctx, client.ScheduleListOptions{})
	if err != nil {
		errs = append(errs, fmt.Errorf("list schedules: %w", err))
		return errors.Join(errs...)
	}
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			errs = append(errs, fmt.Errorf("iterate schedules: %w", err))
			break
		}
		if !strings.HasPrefix(entry.ID, schedulePrefix) {
			continue
		}
		if _, ok := live[entry.ID]; ok {
			continue
		}
		triggerID := strings.TrimPrefix(entry.ID, schedulePrefix)
		// `live` is a snapshot from before the converge loop, so a trigger
		// created since then is absent from it while its schedule is already
		// listed. Re-check the row before deleting anything.
		if _, err := s.repo.GetTrigger(ctx, triggerID); err == nil {
			continue
		} else if !errors.Is(err, core.ErrTriggerNotFound) {
			errs = append(errs, fmt.Errorf("re-check orphan schedule %s: %w", entry.ID, err))
			continue
		}
		if err := s.Delete(ctx, triggerID); err != nil {
			errs = append(errs, fmt.Errorf("delete orphan schedule %s: %w", entry.ID, err))
		}
	}

	return errors.Join(errs...)
}

// scheduleBody is the part of a schedule we own outright: the spec, the
// action, and the policies.
func (s *Syncer) scheduleBody(t *core.Trigger, sched *Schedule) client.Schedule {
	spec := sched.Spec
	return client.Schedule{
		Spec: &spec,
		Action: &client.ScheduleWorkflowAction{
			ID:        FireWorkflowID(t.ID),
			Workflow:  FireWorkflowName,
			Args:      []any{FireInput{TriggerID: t.ID}},
			TaskQueue: s.taskQueue,
			// A fire is bookkeeping plus one launch. If it has not finished in
			// ten minutes something is wrong, and letting it sit forever would
			// hide that behind a schedule that merely looks busy.
			WorkflowExecutionTimeout: fireWorkflowTimeout,
		},
		Policy: &client.SchedulePolicies{
			Overlap:       sched.TemporalOverlap(),
			CatchupWindow: sched.CatchupWindow,
			// Never pause the schedule on a failed action. A trigger stops
			// firing only when a human disables it; a transient launch failure
			// must not silently retire the schedule, because nothing would
			// tell the owner it had stopped.
			PauseOnFailure: false,
		},
	}
}

func scheduleNote(t *core.Trigger) string {
	if t.Enabled {
		return "reliant trigger " + t.Name
	}
	return "reliant trigger " + t.Name + " (disabled)"
}

// ScheduleFor validates and converts a trigger's stored Config.
func ScheduleFor(t *core.Trigger) (*Schedule, error) {
	var cfg core.ScheduleConfig
	if len(t.Config) > 0 {
		if err := json.Unmarshal(t.Config, &cfg); err != nil {
			return nil, &ConfigError{Reason: "config is not a schedule config: " + err.Error()}
		}
	}
	return ParseScheduleConfig(cfg)
}

func isNotFound(err error) bool {
	var nf *serviceerror.NotFound
	return errors.As(err, &nf)
}

func isAlreadyExists(err error) bool {
	var ae *serviceerror.AlreadyExists
	if errors.As(err, &ae) {
		return true
	}
	var wer *serviceerror.WorkflowExecutionAlreadyStarted
	return errors.As(err, &wer)
}
