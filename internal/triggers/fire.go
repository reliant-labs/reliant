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
	repo     Repo
	launcher Launcher
}

// NewFirer builds the fire activity's receiver.
func NewFirer(repo Repo, launcher Launcher) *Firer {
	return &Firer{repo: repo, launcher: launcher}
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

	// A manual fire is a human pressing "run now", so it overrides both the
	// enabled flag and overlap: they are policies for the unattended schedule,
	// not for an explicit request.
	if !req.Manual && !trigger.Enabled {
		return f.recordSkip(ctx, trigger, req, "trigger is disabled")
	}

	if !req.Manual {
		skip, reason, err := f.overlapSkip(ctx, trigger)
		if err != nil {
			return nil, err
		}
		if skip {
			return f.recordSkip(ctx, trigger, req, reason)
		}
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

	spec, err := f.buildSpec(trigger, req)
	if err != nil {
		return nil, err
	}
	ev := f.buildEvent(trigger, req)

	res, launchErr := f.launcher.Launch(ctx, ev, spec)
	switch {
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

// overlapSkip reports whether this trigger's previous run is still going.
//
// "Still going" is the root run being live — active, paused or pending.
// PENDING counts: a chat whose run has not started yet still has all of its
// work ahead of it, so starting a second one would double the work rather than
// replace a stalled first.
func (f *Firer) overlapSkip(ctx context.Context, trigger *core.Trigger) (bool, string, error) {
	sched, err := ScheduleFor(trigger)
	if err != nil {
		// A stored config that no longer parses cannot tell us its overlap
		// policy. Treat it as a validation problem rather than guessing.
		return false, "", nonRetryable("trigger config is invalid: "+err.Error(), err)
	}
	if !sched.SkipOnOverlap() {
		return false, "", nil
	}

	launched := core.TriggerEventLaunched
	prev, err := f.repo.GetLatestTriggerEvent(ctx, trigger.ID, &launched)
	if err != nil {
		if errors.Is(err, core.ErrTriggerEventNotFound) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("load latest launched event for %s: %w", trigger.ID, err)
	}
	if prev == nil || prev.ChatID == nil || *prev.ChatID == "" {
		return false, "", nil
	}

	statuses, err := f.repo.GetRootWorkflowStatusForChats(ctx, []string{*prev.ChatID})
	if err != nil {
		return false, "", fmt.Errorf("load root workflow status for chat %s: %w", *prev.ChatID, err)
	}
	status, ok := statuses[*prev.ChatID]
	if !ok {
		return false, "", nil
	}
	if status.Live() {
		return true, fmt.Sprintf("previous run is %s (chat %s)", status.Label(), *prev.ChatID), nil
	}
	return false, "", nil
}

// buildSpec is what a scheduled run is: owned by the trigger's user,
// unattended, and seeded with the trigger's prompt.
func (f *Firer) buildSpec(trigger *core.Trigger, req FireRequest) (launch.Spec, error) {
	sched, err := ScheduleFor(trigger)
	if err != nil {
		return launch.Spec{}, nonRetryable("trigger config is invalid: "+err.Error(), err)
	}

	local := req.ScheduledAt.In(sched.Location)
	title := fmt.Sprintf("%s · %s", trigger.Name, local.Format("2006-01-02 15:04 MST"))

	params, err := paramsToProto(trigger.Params)
	if err != nil {
		return launch.Spec{}, nonRetryable("trigger params are not representable: "+err.Error(), err)
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
			"trigger_name":  trigger.Name,
			"manual":        req.Manual,
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
	// created=false means a row for this (kind, dedupe_key) already exists —
	// this activity attempt is a retry of one that got this far. The row is
	// the record, so there is nothing left to do and nothing to report as an
	// error.
	if _, err := f.repo.CreateTriggerEvent(ctx, ev); err != nil {
		return nil, fmt.Errorf("record %s event for trigger %s: %w", outcome, trigger.ID, err)
	}
	return &FireOutput{Outcome: string(outcome), Reason: detail, EventID: ev.ID}, nil
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
