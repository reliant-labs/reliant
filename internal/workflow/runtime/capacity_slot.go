// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ── A provider wait gives the workflow-capacity slot back ───────────────
//
// A trigger-launched run of a workflow that declares `concurrency:` holds one
// slot of its scope (store.CanLaunchTriggerCapacity) until it ends. A run that
// waits on a provider — a usage limit, a provider-capacity self-pause, a hold
// for a provider it cannot be served by — is not running, but used to hold its
// slot for the whole wait: in control-plane's pr-review (max_running 6) six
// rate-limited reviews held every slot, and model-free runs queued behind them
// for hours (control-plane#2509).
//
// So a run PARKS its slot when it starts waiting on a provider and no step is
// executing anywhere in it (a run whose other threads still work is still
// running). Parked, it counts toward nothing, and the next queued firing
// starts. It takes the slot back before it dispatches its next step, in any
// thread: that is the invariant — a parked run runs nothing. The resume is
// granted while the scope runs fewer than its cap; otherwise the run asks
// again, and while it asks it is ahead of every queued firing
// (store.ResumeTriggerCapacity), so max_running stays a true upper bound on
// running runs and a resumed run never waits behind newer firings.
//
// Waits for any other reason (approval, ask_user, a user pause, a machine)
// keep their slot.

// capacitySlotChangeID gates parking for histories recorded before it. It is
// consulted at the first provider wait that would park, where the old and new
// shapes first differ; replaying an older history never parks or resumes.
const capacitySlotChangeID = "provider-wait-releases-slot"

// capacityResumeBackoff is how long a parked run waits between asks for its
// slot back while the scope is full. The last rung repeats.
var capacityResumeBackoff = []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, time.Minute}

// capacityResumeMaxFailures is how many failed resume activities (a database
// outage, a worker without the activity) a parked run tolerates before it runs
// anyway: a run wedged forever on infrastructure is worse than one run over
// the cap.
const capacityResumeMaxFailures = 5

// capacitySlot is one run's view of its workflow-capacity slot. Deterministic
// workflow state, one per run, shared by every thread through the
// PauseController. nil for a run that was not trigger-launched, and every
// method is nil-safe.
type capacitySlot struct {
	eventID    string
	workflowID string
	firstRunID string

	// decided/enabled cache this run's answer to capacitySlotChangeID.
	decided bool
	enabled bool
	// none is set once the run is known to hold no slot (not capacity-
	// admitted, or released): it stops asking.
	none bool
	// parked: the slot is given back; the next step takes it again first.
	parked  bool
	parking bool
	// resuming is the resume in progress, which every other thread that wants
	// to dispatch meanwhile waits on instead of starting its own.
	resuming workflow.Future
	// inflight is every dispatched step (StepExecutor.Start) not yet
	// handled. A step whose future is ready is not executing any more, so a
	// step that is never handled (a pause dropped it) cannot keep the run
	// from parking.
	inflight map[*RunningStep]struct{}
}

// newCapacitySlot returns the slot of a trigger-launched run, or nil. parked
// carries a predecessor's parked slot across continue-as-new.
func newCapacitySlot(ctx workflow.Context, trigger *TriggerInfo, parked bool) *capacitySlot {
	if trigger == nil || trigger.EventID == "" {
		return nil
	}
	info := workflow.GetInfo(ctx)
	firstRunID := info.FirstRunID
	if firstRunID == "" {
		// The first run of a chain is its own first run.
		firstRunID = info.WorkflowExecution.RunID
	}
	s := &capacitySlot{
		eventID:    trigger.EventID,
		workflowID: info.WorkflowExecution.ID,
		firstRunID: firstRunID,
		inflight:   map[*RunningStep]struct{}{},
	}
	if parked {
		// Only code that parks hands a parked slot on.
		s.decided, s.enabled, s.parked = true, true, true
	}
	return s
}

// isParked reports whether the run's slot is parked. Nil-safe.
func (s *capacitySlot) isParked() bool { return s != nil && s.parked }

func (s *capacitySlot) trackStep(running *RunningStep) {
	if s == nil || running == nil || running.Future == nil {
		return
	}
	s.inflight[running] = struct{}{}
}

func (s *capacitySlot) untrackStep(running *RunningStep) {
	if s == nil {
		return
	}
	delete(s.inflight, running)
}

// executing reports whether any dispatched step is still executing.
func (s *capacitySlot) executing() bool {
	for running := range s.inflight {
		if !running.Future.IsReady() {
			return true
		}
		delete(s.inflight, running)
	}
	return false
}

func (s *capacitySlot) activityCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
}

func (s *capacitySlot) input(action string) map[string]interface{} {
	return map[string]interface{}{
		"action":       action,
		"event_id":     s.eventID,
		"workflow_id":  s.workflowID,
		"first_run_id": s.firstRunID,
	}
}

// providerWait is called by a thread that starts waiting on a provider. It
// parks the run's slot unless a step is executing elsewhere in the run, the
// slot is already parked, or the run holds none. Best effort: a park that
// fails keeps the slot, as every run did before. ctx MUST be the calling
// coroutine's own context.
func (s *capacitySlot) providerWait(ctx workflow.Context, why string) {
	if s == nil || s.none || s.parked || s.parking || ctx.Err() != nil || s.executing() {
		return
	}
	if !s.decided {
		s.decided = true
		s.enabled = workflow.GetVersion(ctx, capacitySlotChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion
	}
	if !s.enabled {
		return
	}
	s.parking = true
	defer func() { s.parking = false }()
	var out struct {
		Held bool `json:"held"`
	}
	if err := workflow.ExecuteActivity(s.activityCtx(ctx), "TriggerCapacitySlot", s.input("park")).Get(ctx, &out); err != nil {
		workflow.GetLogger(ctx).Warn("[Capacity] Could not give the workflow-capacity slot back during a provider wait; keeping it",
			"workflowID", s.workflowID, "eventID", s.eventID, "error", err)
		return
	}
	if !out.Held {
		s.none = true
		return
	}
	s.parked = true
	if !workflow.IsReplaying(ctx) {
		workflow.GetLogger(ctx).Info("[Capacity] Waiting on a provider; gave the workflow-capacity slot back",
			"workflowID", s.workflowID, "eventID", s.eventID, "why", why)
	}
}

// beforeStep takes a parked slot back before a step dispatches, waiting while
// the scope is full. One resume runs at a time; other threads wait on it.
// ctx MUST be the calling coroutine's own context.
func (s *capacitySlot) beforeStep(ctx workflow.Context) {
	if s == nil || !s.parked {
		return
	}
	if s.resuming != nil {
		_ = s.resuming.Get(ctx, nil)
		return
	}
	future, settable := workflow.NewFuture(ctx)
	s.resuming = future
	defer func() {
		s.resuming = nil
		settable.Set(nil, nil)
	}()
	logger := workflow.GetLogger(ctx)
	failures := 0
	for asks := 0; ; asks++ {
		var out struct {
			Held    bool `json:"held"`
			Granted bool `json:"granted"`
		}
		err := workflow.ExecuteActivity(s.activityCtx(ctx), "TriggerCapacitySlot", s.input("resume")).Get(ctx, &out)
		switch {
		case ctx.Err() != nil:
			// Cancelled: the step this was for fails on the cancel.
			return
		case err != nil:
			failures++
			if failures >= capacityResumeMaxFailures {
				logger.Warn("[Capacity] Could not take the workflow-capacity slot back; running anyway",
					"workflowID", s.workflowID, "eventID", s.eventID, "error", err)
				s.parked = false
				return
			}
		case !out.Held:
			s.parked, s.none = false, true
			return
		case out.Granted:
			s.parked = false
			if !workflow.IsReplaying(ctx) {
				logger.Info("[Capacity] Took the workflow-capacity slot back",
					"workflowID", s.workflowID, "eventID", s.eventID, "asks", asks+1)
			}
			return
		}
		wait := capacityResumeBackoff[min(asks, len(capacityResumeBackoff)-1)]
		if asks == 0 && err == nil && !workflow.IsReplaying(ctx) {
			logger.Info("[Capacity] Workflow capacity is full; waiting ahead of queued firings for a slot",
				"workflowID", s.workflowID, "eventID", s.eventID)
		}
		if workflow.Sleep(ctx, wait) != nil {
			return
		}
	}
}

// capacityProviderWait is capacitySlot.providerWait through a controller.
// Nil-safe.
func (pc *PauseController) capacityProviderWait(ctx workflow.Context, why string) {
	if pc != nil {
		pc.CapacitySlot.providerWait(ctx, why)
	}
}
