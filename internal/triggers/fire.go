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
	capacity  capacityDescriptions
	pools     Placer
}

// NewFirer builds the fire activity's receiver.
func NewFirer(repo Repo, launcher Launcher) *Firer {
	return &Firer{repo: repo, launcher: launcher, now: time.Now}
}

// WithPlacer lets the firer place a pool-bound trigger's runs.
func (f *Firer) WithPlacer(p Placer) *Firer {
	f.pools = p
	return f
}

// WithWorkflows lets the firer read an activation's declaration. Without it,
// an activation's fire is recorded as failed rather than guessed at.
func (f *Firer) WithWorkflows(workflows WorkflowResolver) *Firer {
	f.workflows = workflows
	return f
}

// WithCapacityDescriptions supplies authoritative Temporal run state for an opt-in cap.
func (f *Firer) WithCapacityDescriptions(reader capacityDescriptions) *Firer {
	f.capacity = reader
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
		// A pending row is a fire still waiting: for workflow capacity (it has
		// a capacity order) or for a member of its machine pool (recorded by
		// recordPoolWait). Both continue; anything settled is the answer.
		if existing.Outcome != core.TriggerEventLaunched && existing.Outcome != core.TriggerEventPending {
			return outputFromEvent(existing), nil
		}
	case errors.Is(err, core.ErrTriggerEventNotFound):
		existing = nil
	default:
		return nil, fmt.Errorf("load event for fire %s: %w", req.FireWorkflowID, err)
	}
	resuming := existing != nil && existing.Outcome == core.TriggerEventLaunched

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
	// An activation missing a setting its workflow requires would launch a
	// run that fails input validation at its first step. Record why instead.
	if issues := SetupIssues(decl, trigger); len(issues) > 0 && !resuming {
		return f.recordSkip(ctx, trigger, req, SetupDetail(issues))
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
	// credential its runs hold is bound to it. Record why and stop. A
	// no-machine trigger names none, by design (research/DAEMONLESS_RUNS.md).
	if trigger.NoMachine || trigger.MachinePoolID != "" {
		// nothing to look up: a pool-bound trigger is placed at launch
	} else if _, err := f.repo.GetDaemon(ctx, trigger.DaemonID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("load daemon %s: %w", trigger.DaemonID, err)
		}
		const detail = "trigger's daemon no longer exists; edit the trigger to choose another"
		if _, err := f.recordOutcome(ctx, trigger, req, core.TriggerEventFailed, detail); err != nil {
			return nil, err
		}
		return nil, nonRetryable(detail, err)
	}

	spec, err := f.buildSpec(ctx, trigger, sched, req, decl)
	if err != nil {
		return f.failPermanently(ctx, trigger, req, err.Error(), err)
	}
	policy, err := capacityPolicy(ctx, f.workflows, trigger)
	if err != nil {
		return nil, err
	}
	maxRunning, capped, err := capacityLimit(ctx, f.pools, trigger, policy)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Outcome == core.TriggerEventPending && existing.CapacityOrder != nil && !capped {
		return f.recordSkip(ctx, trigger, req, "queued workflow capacity policy was removed")
	}
	if !capped && enforcePolicy && sched.SkipOnOverlap() {
		// Checked inside the launch transaction under a row lock on the
		// trigger, so fires released together after an outage cannot all
		// pass the check before any of them has launched.
		spec.Guard = f.overlapGuard(trigger.ID)
	}
	ev := f.buildEvent(trigger, req)
	if capped {
		row := &core.TriggerEvent{ID: uuid.NewString(), UserID: trigger.UserID, TriggerID: &trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey, OccurredAt: ev.OccurredAt, CreatedAt: f.now(), Payload: ev.Payload, Outcome: core.TriggerEventPending}
		if existing != nil {
			row = existing
		}
		if err := prepareCapacity(ctx, f.repo, f.capacity, trigger, row, maxRunning, &spec); err != nil {
			if errors.Is(err, errCapacityScopeChanged) {
				return f.recordSkip(ctx, trigger, req, err.Error())
			}
			return nil, err
		}
	}

	if existing != nil && existing.CapacityOrder != nil && existing.Outcome == core.TriggerEventLaunched && spec.RecordExecution == nil {
		if repo, ok := f.repo.(capacityRepository); ok {
			spec.RecordExecution = repo.BindTriggerCapacityExecution
			spec.PrepareExecution = repo.PrepareTriggerCapacityExecution
		} else {
			return nil, errors.New("admitted retry storage unavailable")
		}
	}
	res, launchErr := launchOnPool(ctx, f.pools, f.launcher, trigger, ev, spec, resuming)
	switch {
	case errors.Is(launchErr, launch.ErrDeclined):
		var declined *launch.DeclinedError
		reason := launchErr.Error()
		if errors.As(launchErr, &declined) {
			reason = declined.Reason
		}
		if reason == capacityWaiting {
			return &FireOutput{Outcome: string(core.TriggerEventPending), Reason: reason}, nil
		}
		if isPoolWaiting(reason) {
			return f.recordPoolWait(ctx, trigger, ev, reason)
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
// "Still going" is the root run being active or paused, or PENDING with its
// start in flight: a fire that has just committed is PENDING until its
// Temporal start marks it ACTIVE, and the fire queued on the trigger lock
// behind it reads it in exactly that window. Only a root whose start is
// recorded as failed (Workflow.StartFailedAt — by the launcher's error branch,
// or by the reconciler once the start has been missing from Temporal for its
// bound) is stranded, and that one must not wedge the trigger: this fire
// abandons it and launches. The abandon is conditional on the stamp, so a
// retry of the stranded fire that has just claimed its start again wins, and
// this fire skips instead of running beside it.
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
	// The run this trigger fired has ended. Anything still live in its chat is
	// a person's own turn, which is theirs to have and holds nothing up.
	if prev.RunStatus != "" {
		return "", nil
	}

	statuses, err := f.repo.GetRootWorkflowStatusForChats(ctx, []string{*prev.ChatID})
	if err != nil {
		return "", fmt.Errorf("load root workflow status for chat %s: %w", *prev.ChatID, err)
	}
	status, ok := statuses[*prev.ChatID]
	if !ok {
		return "", nil
	}
	if status.State == core.WorkflowStatePending {
		return f.pendingRunBlocks(ctx, *prev.ChatID)
	}
	if status.Live() {
		return fmt.Sprintf("previous run is %s (chat %s)", status.Label(), *prev.ChatID), nil
	}
	return "", nil
}

// pendingRunBlocks decides a previous fire whose root is still PENDING: in
// flight blocks, a recorded failed start is abandoned and does not.
func (f *Firer) pendingRunBlocks(ctx context.Context, chatID string) (string, error) {
	starting := fmt.Sprintf("previous run is starting (chat %s)", chatID)
	// A chat's effective status is PENDING only when its newest root is.
	roots, err := f.repo.ListRootWorkflows(ctx, chatID)
	if err != nil {
		return "", fmt.Errorf("load root workflow of chat %s: %w", chatID, err)
	}
	if len(roots) == 0 {
		return "", nil
	}
	root := roots[0]
	if root.Status.State != core.WorkflowStatePending {
		// Moved on between the two reads; it is no longer PENDING, so this
		// fire treats it as the conservative case and skips.
		return starting, nil
	}
	if root.StartFailedAt == nil {
		return starting, nil
	}
	abandoned, err := f.repo.AbandonFailedWorkflowStart(ctx, root.ID)
	if err != nil {
		return "", fmt.Errorf("abandon the failed start of chat %s: %w", chatID, err)
	}
	if !abandoned {
		// A retry of that fire claimed its start again after the read above.
		return starting, nil
	}
	return "", nil
}

// buildSpec is what a scheduled run is: owned by the trigger's user,
// unattended, and seeded with the trigger's prompt. decl is the declaration
// an activation fires from, nil for an ad hoc trigger.
func (f *Firer) buildSpec(ctx context.Context, trigger *core.Trigger, sched *Schedule, req FireRequest, decl *Declaration) (launch.Spec, error) {
	local := req.ScheduledAt.In(sched.Location)
	title := fmt.Sprintf("%s · %s", trigger.Name, local.Format("2006-01-02 15:04 MST"))

	// The same root the run will see as `trigger`: what a schedule's inputs
	// and prompt can read is its slot (trigger.scheduled_for) and name.
	ev := f.buildEvent(trigger, req)
	scope := ActivationScope(ctx, f.repo, trigger, decl, FilterInput{
		Kind: string(core.TriggerEventKindSchedule), TriggerID: trigger.ID,
		OccurredAt: req.ScheduledAt, Payload: ev.Payload, Sender: ev.Sender,
	}.Root())
	values := trigger.Params
	if decl != nil && len(decl.Inputs) > 0 {
		merged, err := MergeDeclaredInputs(trigger.Params, decl.Inputs, scope)
		if err != nil {
			return launch.Spec{}, fmt.Errorf("the declared trigger's inputs could not be evaluated: %w", err)
		}
		values = merged
	}
	prompt, err := SeedPrompt(trigger, decl, scope)
	if err != nil {
		return launch.Spec{}, err
	}
	params, err := paramsToProto(values)
	if err != nil {
		return launch.Spec{}, fmt.Errorf("trigger params are not representable: %w", err)
	}

	hidden := reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN
	messages := []launch.SeedMessage{
		{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: prompt,
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
		NoMachine:   trigger.NoMachine,
		Presets:     trigger.Presets,
		Params:      params,
		Messages:    messages,

		Unattended:     true,
		ActingIdentity: trigger.ActingIdentity,
		Automation:     trigger.Name,
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
		Sender: scheduleSender(trigger),
	}
}

// scheduleSender is a schedule's trigger.sender: its owner, who set it up.
// Nobody outside can send a schedule an event, so it is verified.
func scheduleSender(trigger *core.Trigger) *core.TriggerSender {
	return &core.TriggerSender{Kind: core.TriggerSenderKindSchedule, ID: trigger.UserID, Verified: true}
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
		Sender:        scheduleSender(trigger),
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
		if stored.Outcome == core.TriggerEventPending && stored.CapacityOrder != nil {
			settler, ok := f.repo.(interface {
				SettlePendingTriggerEvent(context.Context, string, core.TriggerEventOutcome, string) (bool, error)
			})
			if !ok {
				return nil, errors.New("capacity queue settlement unavailable")
			}
			if _, err := settler.SettlePendingTriggerEvent(ctx, stored.ID, outcome, detail); err != nil {
				return nil, err
			}
			stored, err = f.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, req.FireWorkflowID)
			if err != nil {
				return nil, err
			}
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
