// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The inbound path: how an event that ARRIVES (a webhook delivery, a provider
// event, a poll result, another run's outcome) becomes a run.
//
// It is split across the two processes on purpose. The receiver runs on the
// api-server, which a sender is waiting on — GitHub gives up after 10s and
// never retries, Slack after 3s — so it does only what must be synchronous:
// decide whether the event is for this trigger, write the trigger_events row,
// and start the fire workflow. The launch (chat, root workflow, Temporal
// start) happens on the worker, exactly as a scheduled fire's does.
//
//	receiver ──► Intake.Accept ──► trigger_events row (pending | skipped | failed)
//	                         └───► EventFireWorkflow (id = event id)
//	worker   ──► EventFirer.Fire ─► launch.Launch adopts the pending row
//	              Redriver ───────► restarts the fire of a row left pending
//
// The row is written BEFORE the start, so a crash between the two leaves a
// pending row the Redriver finds — the event is never lost — and the
// (kind, dedupe_key) constraint makes every repeat a no-op.

// InboundEvent is an arrived event, ready to record.
type InboundEvent struct {
	// Kind is the trigger_events kind: webhook, integration or
	// workflow_event.
	Kind core.TriggerEventKind
	// DedupeKey must be unique per (trigger, real-world event): a redelivery
	// of the same event carries the same key. Convention:
	// "<trigger id>:<source's own id for the event>".
	DedupeKey string
	// OccurredAt is when the source says the event happened; zero means now.
	OccurredAt time.Time
	// Payload is recorded verbatim on the row and becomes trigger.payload.
	// It is untrusted input: callers size-cap it and strip secrets first.
	Payload map[string]any
	// Sender is trigger.sender, normalized by the receiver from what the
	// source authenticated (core.TriggerSender). Required: an event nobody
	// sent cannot be filtered by who sent it.
	Sender *core.TriggerSender
}

// AcceptOptions modify Accept.
type AcceptOptions struct {
	// Manual is a human's "run now": it bypasses the filter, the enabled
	// flag and nothing else.
	Manual bool
}

// AcceptResult is what Accept recorded.
type AcceptResult struct {
	EventID string
	Outcome core.TriggerEventOutcome
	Detail  string
	// Duplicate means a row for this (kind, dedupe key) already existed and
	// nothing new was written. EventID and Outcome are that row's.
	Duplicate bool
}

// Intake records arrived events and starts their fires.
type Intake struct {
	repo      EventRepo
	starter   WorkflowStarter
	taskQueue string
	workflows WorkflowResolver
	now       func() time.Time
}

// WithWorkflows lets the intake read an activation's declaration, whose
// filter is the one applied. Without it, an activation's events are recorded
// as failed rather than filtered by a projection that may be stale.
func (in *Intake) WithWorkflows(workflows WorkflowResolver) *Intake {
	in.workflows = workflows
	return in
}

// NewIntake builds an Intake. taskQueue empty means the shared queue.
func NewIntake(repo EventRepo, starter WorkflowStarter, taskQueue string) *Intake {
	if taskQueue == "" {
		taskQueue = defaultTaskQueue
	}
	return &Intake{repo: repo, starter: starter, taskQueue: taskQueue, now: time.Now}
}

// Accept records ev for trigger and, when it passes the trigger's filter,
// starts the fire that will launch it.
//
// The caller has already decided the event is for this trigger by its type
// and source config: a mismatch writes NO row, because a busy source would
// otherwise bury the trigger's history in events it never listened to. The
// filter is different — it is the owner's own condition, and a miss is
// recorded as skipped so "why did my trigger not fire?" has an answer.
//
// A filter that cannot evaluate (it reads a key this payload lacks) records
// failed, not skipped: that is the owner's filter being wrong for the data,
// which is something to fix rather than an ordinary "not for me".
//
// A failure to START the fire is not an error: the row is the durable
// record, the Redriver repairs the start, and the sender must not be told to
// retry something that was accepted.
func (in *Intake) Accept(ctx context.Context, trigger *core.Trigger, ev InboundEvent, opts AcceptOptions) (*AcceptResult, error) {
	if trigger == nil {
		return nil, errors.New("triggers: accept needs a trigger")
	}
	if !ev.Kind.IsInbound() {
		return nil, fmt.Errorf("triggers: %q is not an inbound event kind", ev.Kind)
	}
	if ev.DedupeKey == "" {
		return nil, errors.New("triggers: an inbound event needs a dedupe key")
	}
	if ev.Sender == nil {
		// Every receiver knows who authenticated its request; one that
		// forgot to say would record an event an allowlist can never pass,
		// with no hint why.
		return nil, errors.New("triggers: an inbound event needs a sender")
	}
	occurredAt := ev.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = in.now().UTC()
	}

	outcome, detail := core.TriggerEventPending, ""
	decl, err := activationFor(ctx, in.workflows, trigger)
	switch {
	case err != nil && isVerdict(err):
		// Recorded, not dropped: "why did my trigger stop firing?" must have
		// an answer, and this is it.
		outcome, detail = core.TriggerEventFailed, err.Error()
	case err != nil:
		return nil, fmt.Errorf("resolve trigger %s's declaration: %w", trigger.ID, err)
	case !opts.Manual:
		filter := trigger.Filter
		if decl != nil {
			filter = decl.Filter
		}
		outcome, detail = in.applyFilter(filter, trigger, ev, occurredAt)
	}

	row := &core.TriggerEvent{
		ID:            uuid.NewString(),
		TriggerID:     &trigger.ID,
		UserID:        trigger.UserID,
		Kind:          ev.Kind,
		DedupeKey:     ev.DedupeKey,
		OccurredAt:    occurredAt,
		Payload:       ev.Payload,
		Sender:        ev.Sender,
		Outcome:       outcome,
		OutcomeDetail: detail,
		CreatedAt:     in.now().UTC(),
	}
	created, err := in.repo.CreateTriggerEvent(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("record %s event for trigger %s: %w", ev.Kind, trigger.ID, err)
	}
	res := &AcceptResult{EventID: row.ID, Outcome: outcome, Detail: detail}
	if !created {
		stored, err := in.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey)
		if err != nil {
			return nil, fmt.Errorf("load existing event %s: %w", ev.DedupeKey, err)
		}
		res = &AcceptResult{EventID: stored.ID, Outcome: stored.Outcome, Detail: stored.OutcomeDetail, Duplicate: true}
		if stored.Outcome != core.TriggerEventPending {
			return res, nil
		}
		// Still pending: the first delivery's start may have failed, so
		// (re)starting is the repair. The fire id is the event's, so if it
		// is already running this attaches rather than duplicating.
	} else if outcome != core.TriggerEventPending {
		return res, nil
	}

	input := EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey, Manual: opts.Manual}
	if err := StartEventFire(ctx, in.starter, res.EventID, input, in.taskQueue); err != nil {
		logging.Warn("could not start the fire of an accepted trigger event; the redriver will retry",
			"trigger_id", trigger.ID, "event_id", res.EventID, "error", err)
	}
	return res, nil
}

func (in *Intake) applyFilter(expr string, trigger *core.Trigger, ev InboundEvent, occurredAt time.Time) (core.TriggerEventOutcome, string) {
	if strings.TrimSpace(expr) == "" {
		return core.TriggerEventPending, ""
	}
	filter, err := CompileFilter(expr)
	if err != nil {
		// Validated on write, so this is a filter stored before a rule
		// tightened. Record it; never fall back to firing.
		return core.TriggerEventFailed, "the trigger's filter does not compile: " + err.Error()
	}
	hit, err := filter.Match(FilterInput{
		Kind: string(ev.Kind), TriggerID: trigger.ID, OccurredAt: occurredAt, Payload: ev.Payload, Sender: ev.Sender,
	})
	if err != nil {
		return core.TriggerEventFailed, fmt.Sprintf(
			"the trigger's filter could not evaluate this event (%v); guard optional fields with has()", err)
	}
	if !hit {
		return core.TriggerEventSkipped, "filter did not match: " + filter.Expr()
	}
	return core.TriggerEventPending, ""
}

// IntegrationEventMatches reports whether an integration event of eventType
// with attrs is one cfg listens to. This is the pre-row gate: a false here
// writes nothing.
//
// events: an exact type, "type.*" for every action of a type, or "*" for
// everything. match: every entry must equal the event's attribute; an absent
// attribute is a miss, so a trigger scoped to one repository can never fire
// on an event that does not say which repository it is about.
func IntegrationEventMatches(cfg core.IntegrationConfig, eventType string, attrs map[string]string) bool {
	matched := false
	for _, want := range cfg.Events {
		switch {
		case want == "*" || want == eventType:
			matched = true
		case strings.HasSuffix(want, ".*"):
			matched = strings.HasPrefix(eventType, strings.TrimSuffix(want, "*"))
		}
		if matched {
			break
		}
	}
	if !matched {
		return false
	}
	for key, want := range cfg.Match {
		got, ok := attrs[key]
		if !ok || got != want {
			return false
		}
	}
	return true
}

// EventFireWorkflowID is the fire workflow id for one recorded event. It is
// derived from the event id, so every start for the event — the receiver's,
// a redelivery's, the redriver's — names the same execution.
func EventFireWorkflowID(eventID string) string { return schedulePrefix + "event-" + eventID }

// StartEventFire starts the fire workflow for a recorded event. An execution
// that is already running or has completed for this id is success: the start
// is idempotent by construction.
func StartEventFire(ctx context.Context, starter WorkflowStarter, eventID string, input EventFireInput, taskQueue string) error {
	if taskQueue == "" {
		taskQueue = defaultTaskQueue
	}
	_, err := starter.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       EventFireWorkflowID(eventID),
		TaskQueue:                taskQueue,
		WorkflowExecutionTimeout: fireWorkflowTimeout,
		RetryPolicy:              &temporal.RetryPolicy{MaximumAttempts: 1},
	}, EventFireWorkflowName, input)
	if err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("start fire for event %s: %w", eventID, err)
	}
	return nil
}
