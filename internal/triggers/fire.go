// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// chatIDNamespace seeds the deterministic chat id a scheduled fire launches
// into. A fixed namespace plus the fire workflow id means a retried fire
// derives the SAME chat id, so the launcher's own idempotency on NewChatID
// catches a duplicate even if the event row write and the launch were split by
// a crash.
//
// Do not change this value: it would re-point every future fire of every
// existing trigger at a different chat id, and in-flight fires retried across
// the change would launch twice.
var chatIDNamespace = uuid.MustParse("2b2e5f21-6c3d-4f8a-9c41-7e5a0d9b3c66")

// Firer executes one scheduled fire. It is the FireTrigger activity.
type Firer struct {
	repo      Repo
	launcher  Launcher
	workflows WorkflowResolver
	now       func() time.Time
}

// NewFirer builds the fire activity's receiver.
func NewFirer(repo Repo, launcher Launcher) *Firer {
	return &Firer{repo: repo, launcher: launcher, now: time.Now}
}

// WithWorkflows lets the firer read an activation's declaration. Without it,
// an activation's fire is recorded as failed rather than guessed at.
func (f *Firer) WithWorkflows(workflows WorkflowResolver) *Firer {
	f.workflows = workflows
	return f
}

// Fire is the FireTrigger activity.
//
// The order is load, decide, launch — and the decision to skip is RECORDED,
// not merely acted on. A trigger that stopped producing runs has to be
// explicable after the fact, and "there is no event row" cannot distinguish a
// skipped fire from a schedule that never fired at all.
func (f *Firer) Fire(ctx context.Context, req FireRequest) (*FireOutput, error) {
	if req.TriggerID == "" {
		return nil, nonRetryable("fire has no trigger id", nil)
	}
	if req.FireWorkflowID == "" {
		return nil, nonRetryable("fire has no workflow id", nil)
	}

	// This fire's own identity decides first. A retry of a fire that already
	// got an event row must continue THAT fire: re-running the enabled and
	// overlap policy against it would compare the fire with its own
	// half-launched chat and skip itself, wedging the trigger.
	existing, err := f.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, req.FireWorkflowID)
	switch {
	case err == nil:
		if existing.Outcome != core.TriggerEventLaunched {
			return outputFromEvent(existing), nil
		}
	case errors.Is(err, core.ErrTriggerEventNotFound):
		existing = nil
	default:
		return nil, fmt.Errorf("load event for fire %s: %w", req.FireWorkflowID, err)
	}
	resuming := existing != nil

	trigger, err := f.repo.GetTrigger(ctx, req.TriggerID)
	if err != nil {
		if errors.Is(err, core.ErrTriggerNotFound) {
			// The trigger was deleted between the schedule firing and the
			// activity running. There is no user or trigger to attribute an
			// event row to, and the orphan schedule is SyncAll's job, so the
			// fire simply ends.
			return &FireOutput{Outcome: string(core.TriggerEventSkipped), Reason: "trigger deleted"}, nil
		}
		return nil, fmt.Errorf("load trigger %s: %w", req.TriggerID, err)
	}

	// An activation fires from its workflow's declaration as it is NOW: the
	// row's config is only the projection the schedule was converged from.
	// A declaration that is gone or changed kind is recorded and stops here;
	// one whose cron or timezone was edited fires this slot under the new
	// rules (the reconciler re-converges the Temporal schedule itself).
	decl, err := activationFor(ctx, f.workflows, trigger)
	if err != nil {
		if isVerdict(err) {
			return f.failPermanently(ctx, trigger, req, err.Error(), err)
		}
		return nil, err
	}
	if decl != nil {
		projected := *trigger
		projected.Config = decl.Source.Config
		trigger = &projected
	}
	inputs := map[string]string(nil)
	if decl != nil {
		inputs = decl.Inputs
	}

	// A stored config that no longer parses cannot tell us its overlap policy
	// or timezone. Record that as the verdict rather than guessing, so the
	// owner can see why the trigger stopped producing runs.
	sched, err := ScheduleFor(trigger)
	if err != nil {
		return f.failPermanently(ctx, trigger, req, "trigger config is invalid: "+err.Error(), err)
	}

	// A manual fire is a human pressing "run now", so it overrides both the
	// enabled flag and overlap: they are policies for the unattended schedule,
	// not for an explicit request. A resumed fire already passed both.
	enforcePolicy := !req.Manual && !resuming
	if enforcePolicy && !trigger.Enabled {
		return f.recordSkip(ctx, trigger, req, "trigger is disabled")
	}

	// Never fall back to another daemon: the trigger named this one, and the
	// credential its runs hold is bound to it. Record why and stop.
	if _, err := f.repo.GetDaemon(ctx, trigger.DaemonID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("load daemon %s: %w", trigger.DaemonID, err)
		}
		const detail = "trigger's daemon no longer exists; edit the trigger to choose another"
		if _, err := f.recordOutcome(ctx, trigger, req, core.TriggerEventFailed, detail); err != nil {
			return nil, err
		}
		return nil, nonRetryable(detail, err)
	}

	spec, err := f.buildSpec(trigger, sched, req, inputs)
	if err != nil {
		return f.failPermanently(ctx, trigger, req, err.Error(), err)
	}
	if enforcePolicy && sched.SkipOnOverlap() {
		// Checked inside the launch transaction under a row lock on the
		// trigger, so fires released together after an outage cannot all
		// pass the check before any of them has launched.
		spec.Guard = f.overlapGuard(trigger.ID)
	}
	ev := f.buildEvent(trigger, req)

	res, launchErr := f.launcher.Launch(ctx, ev, spec)
	switch {
	case errors.Is(launchErr, launch.ErrDeclined):
		var declined *launch.DeclinedError
		reason := launchErr.Error()
		if errors.As(launchErr, &declined) {
			reason = declined.Reason
		}
		return f.recordSkip(ctx, trigger, req, reason)

	case launchErr == nil:
		// Launch recorded the launched event itself, inside the same
		// transaction as the chat. Writing one here too would either conflict
		// on (kind, dedupe_key) or, worse, succeed and double-count.
		return &FireOutput{
			Outcome: string(core.TriggerEventLaunched),
			ChatID:  res.Chat.ID,
			EventID: res.EventID,
		}, nil

	case errors.Is(launchErr, launch.ErrAlreadyLaunched):
		// A retry of a fire that already launched. The desired state holds, so
		// this is success, not an error to surface to the schedule.
		var already *launch.AlreadyLaunchedError
		out := &FireOutput{Outcome: string(core.TriggerEventLaunched), Reason: "already launched"}
		if errors.As(launchErr, &already) {
			out.ChatID = already.ChatID
			out.EventID = already.EventID
		}
		return out, nil
	}

	var validation *launch.ValidationError
	if errors.As(launchErr, &validation) || errors.Is(launchErr, launch.ErrNotFound) {
		// The spec can never launch as written — a workflow that no longer
		// exists, a project that was deleted. Record the verdict so the owner
		// can see WHY their trigger stopped producing runs, then stop:
		// retrying would re-reach the same conclusion every backoff interval
		// until the workflow timed out.
		if _, err := f.recordOutcome(ctx, trigger, req, core.TriggerEventFailed, launchErr.Error()); err != nil {
			return nil, err
		}
		return nil, nonRetryable("trigger cannot launch: "+launchErr.Error(), launchErr)
	}

	return nil, fmt.Errorf("launch trigger %s: %w", trigger.ID, launchErr)
}

// overlapGuard is the launch guard for overlap=skip. It runs in the launch
// transaction: locking the trigger row first serializes concurrent fires of
// the same trigger, so each sees the previous one's committed launch.
func (f *Firer) overlapGuard(triggerID string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if err := f.repo.LockTrigger(ctx, triggerID); err != nil {
			if errors.Is(err, core.ErrTriggerNotFound) {
				return "trigger deleted", nil
			}
			return "", fmt.Errorf("lock trigger %s: %w", triggerID, err)
		}
		return f.previousRunBlocks(ctx, triggerID)
	}
}

// previousRunBlocks reports whether this trigger's previous run is still going.
//
// "Still going" is the root run being active or paused. PENDING does not
// count: a pending root has never started, which for a launched event means a
// half-launched fire whose Temporal start failed — it is stranded, not
// running, and treating it as live would wedge the trigger behind it forever.
func (f *Firer) previousRunBlocks(ctx context.Context, triggerID string) (string, error) {
	launched := core.TriggerEventLaunched
	prev, err := f.repo.GetLatestTriggerEvent(ctx, triggerID, &launched)
	if err != nil {
		if errors.Is(err, core.ErrTriggerEventNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("load latest launched event for %s: %w", triggerID, err)
	}
	if prev == nil || prev.ChatID == nil || *prev.ChatID == "" {
		return "", nil
	}

	statuses, err := f.repo.GetRootWorkflowStatusForChats(ctx, []string{*prev.ChatID})
	if err != nil {
		return "", fmt.Errorf("load root workflow status for chat %s: %w", *prev.ChatID, err)
	}
	status, ok := statuses[*prev.ChatID]
	if !ok || status.State == core.WorkflowStatePending {
		return "", nil
	}
	if status.Live() {
		return fmt.Sprintf("previous run is %s (chat %s)", status.Label(), *prev.ChatID), nil
	}
	return "", nil
}

// buildSpec is what a scheduled run is: owned by the trigger's user,
// unattended, and seeded with the trigger's prompt.
func (f *Firer) buildSpec(trigger *core.Trigger, sched *Schedule, req FireRequest, inputs map[string]string) (launch.Spec, error) {
	local := req.ScheduledAt.In(sched.Location)
	title := fmt.Sprintf("%s · %s", trigger.Name, local.Format("2006-01-02 15:04 MST"))

	values := trigger.Params
	if len(inputs) > 0 {
		// The same root the run will see as `trigger`: what a schedule's
		// inputs can read is its slot (trigger.scheduled_for) and name.
		root := FilterInput{
			Kind: string(core.TriggerEventKindSchedule), TriggerID: trigger.ID,
			OccurredAt: req.ScheduledAt, Payload: f.buildEvent(trigger, req).Payload,
		}.Root()
		merged, err := MergeDeclaredInputs(trigger.Params, inputs, root)
		if err != nil {
			return launch.Spec{}, fmt.Errorf("the declared trigger's inputs could not be evaluated: %w", err)
		}
		values = merged
	}
	params, err := paramsToProto(values)
	if err != nil {
		return launch.Spec{}, fmt.Errorf("trigger params are not representable: %w", err)
	}

	hidden := reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN
	messages := []launch.SeedMessage{
		{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: trigger.Message,
		},
		{
			// The agent needs to know it is unattended, and it cannot infer
			// that from inputs it never sees. Hidden because it is context for
			// the model, not something a human reading the transcript later
			// needs in the conversation.
			Role: reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM,
			Content: fmt.Sprintf(
				"This run was started automatically by the schedule %q for %s. "+
					"No human is watching: nobody will answer a question or approve a request, "+
					"so decide and proceed, and record anything that needs review in your final response.",
				trigger.Name, local.Format(time.RFC1123),
			),
			DisplayStyle: &hidden,
		},
	}

	return launch.Spec{
		OwnerUserID: trigger.UserID,
		ProjectID:   trigger.ProjectID,
		WorktreeID:  trigger.WorktreeID,
		NewChatID:   uuid.NewSHA1(chatIDNamespace, []byte(req.FireWorkflowID)).String(),
		Title:       &title,
		Workflow:    trigger.Workflow,
		DaemonID:    trigger.DaemonID,
		Presets:     trigger.Presets,
		Params:      params,
		Messages:    messages,

		Unattended: true,
		// The title is already derived from the trigger's name and the
		// scheduled time, which is more useful for an automation run than an
		// LLM's summary of its own prompt — and it costs no model call.
		GenerateTitle: false,
		// The greenfield probe asks the daemon whether the working directory
		// holds code, to coach a human through their first commit. A schedule
		// fires against a project that already exists, and there is no human
		// to coach.
		GreenfieldProbe: false,
	}, nil
}

func (f *Firer) buildEvent(trigger *core.Trigger, req FireRequest) launch.Event {
	return launch.Event{
		Kind:      core.TriggerEventKindSchedule,
		TriggerID: trigger.ID,
		// The fire workflow id is unique per scheduled time, so it is both the
		// natural dedupe key and the reason a retried fire is harmless.
		DedupeKey:  req.FireWorkflowID,
		OccurredAt: req.ScheduledAt,
		Payload: map[string]any{
			"scheduled_for": req.ScheduledAt.UTC().Format(time.RFC3339),
			// Wall-clock time of this activity attempt (not workflow code, so
			// time.Now is fine). The event row is written once, so a retried
			// fire keeps the first attempt's time.
			"fired_at":     f.now().UTC().Format(time.RFC3339),
			"trigger_name": trigger.Name,
			"manual":       req.Manual,
		},
	}
}

func (f *Firer) recordSkip(ctx context.Context, trigger *core.Trigger, req FireRequest, reason string) (*FireOutput, error) {
	return f.recordOutcome(ctx, trigger, req, core.TriggerEventSkipped, reason)
}

// recordOutcome writes the event row for a fire that did NOT launch. Launched
// events are written by the launcher, in the chat's transaction; these are the
// outcomes it never sees.
func (f *Firer) recordOutcome(
	ctx context.Context,
	trigger *core.Trigger,
	req FireRequest,
	outcome core.TriggerEventOutcome,
	detail string,
) (*FireOutput, error) {
	ev := &core.TriggerEvent{
		ID:            uuid.NewString(),
		TriggerID:     &trigger.ID,
		UserID:        trigger.UserID,
		Kind:          core.TriggerEventKindSchedule,
		DedupeKey:     req.FireWorkflowID,
		OccurredAt:    req.ScheduledAt,
		Payload:       f.buildEvent(trigger, req).Payload,
		Outcome:       outcome,
		OutcomeDetail: detail,
	}
	created, err := f.repo.CreateTriggerEvent(ctx, ev)
	if err != nil {
		return nil, fmt.Errorf("record %s event for trigger %s: %w", outcome, trigger.ID, err)
	}
	if !created {
		// A row for this (kind, dedupe_key) already exists: this attempt is a
		// retry of one that got this far, or lost a race. The row is the
		// record, so report IT rather than an id that was never written.
		stored, err := f.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, req.FireWorkflowID)
		if err != nil {
			return nil, fmt.Errorf("load existing event for fire %s: %w", req.FireWorkflowID, err)
		}
		return outputFromEvent(stored), nil
	}
	return &FireOutput{Outcome: string(outcome), Reason: detail, EventID: ev.ID}, nil
}

// failPermanently records a failed event and ends the fire without retries:
// the trigger can never launch as written.
func (f *Firer) failPermanently(ctx context.Context, trigger *core.Trigger, req FireRequest, detail string, cause error) (*FireOutput, error) {
	if _, err := f.recordOutcome(ctx, trigger, req, core.TriggerEventFailed, detail); err != nil {
		return nil, err
	}
	return nil, nonRetryable(detail, cause)
}

func outputFromEvent(ev *core.TriggerEvent) *FireOutput {
	out := &FireOutput{Outcome: string(ev.Outcome), Reason: ev.OutcomeDetail, EventID: ev.ID}
	if ev.ChatID != nil {
		out.ChatID = *ev.ChatID
	}
	return out
}

func paramsToProto(params map[string]any) (map[string]*structpb.Value, error) {
	if len(params) == 0 {
		return nil, nil
	}
	out := make(map[string]*structpb.Value, len(params))
	for k, v := range params {
		val, err := structpb.NewValue(v)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}

func nonRetryable(msg string, cause error) error {
	return temporal.NewNonRetryableApplicationError(msg, nonRetryableFireError, cause)
}
