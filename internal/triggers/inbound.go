// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
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
	// Echo is what the event says about itself, for attributing it to a write
	// Reliant made (see Enricher). Evidence only: it is never recorded and never
	// reaches a filter. nil for a source that has none.
	Echo *provenance.Echo
}

// AcceptOptions modify Accept.
type AcceptOptions struct {
	// Manual is a human's "run now": it bypasses the filter, the enabled
	// flag and nothing else.
	Manual bool
	// Batch, when set, is shared by every Accept of one routing attempt that
	// reaches several triggers so each catalog scope is pinned and read once.
	Batch *Batch
	// Receipt links a targeted durable receipt to its first event atomically.
	Receipt *IntakeReceipt
}

// IntakeReceipt fences event creation when processing one durable webhook.
type IntakeReceipt struct {
	ID, LeaseToken, EventID string
	ReceivedAt              time.Time
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
	repo      IntakeRepo
	starter   WorkflowStarter
	taskQueue string
	workflows WorkflowResolver
	pools     PoolCatalogs
	enrichers []Enricher
	now       func() time.Time
}

// WithPoolCatalogs lets the intake read a pool-bound trigger's declaration
// from its pool's agreed catalog. Without it, Accept refuses such a
// trigger's events with an error (a wiring bug) rather than filtering them
// against the union.
func (in *Intake) WithPoolCatalogs(pools PoolCatalogs) *Intake {
	in.pools = pools
	return in
}

// WithWorkflows lets the intake read an activation's declaration, whose
// filter is the one applied. Without it, an activation's events are recorded
// as failed rather than filtered by a projection that may be stale.
func (in *Intake) WithWorkflows(workflows WorkflowResolver) *Intake {
	in.workflows = workflows
	return in
}

// NewIntake builds an Intake. taskQueue empty means the shared queue.
func NewIntake(repo IntakeRepo, starter WorkflowStarter, taskQueue string) *Intake {
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
	began := time.Now()
	// The owner reads every filter scope makes (OwnerOf) are the same for
	// all the triggers of one arrival: read them once.
	in = in.inBatch(opts.Batch)
	if ev.Sender == nil {
		// Every receiver knows who authenticated its request; one that
		// forgot to say would record an event an allowlist can never pass,
		// with no hint why.
		return nil, errors.New("triggers: an inbound event needs a sender")
	}
	if receipt := opts.Receipt; receipt != nil {
		if receipt.EventID != "" {
			stored, err := in.repo.TriggerDeliveryEvent(ctx, receipt.EventID, trigger.ID, trigger.UserID)
			if err != nil {
				return nil, err
			}
			return in.startRecordedEvent(ctx, trigger.ID, stored, true, opts.Manual), nil
		}
	}
	occurredAt := ev.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = in.now().UTC()
	}

	outcome, detail := core.TriggerEventPending, ""
	// A pool-bound trigger's filter and dedupe key are its pool's agreed
	// catalog's, the one its run is placed on. With no member synced there is
	// nothing to filter against, which is recorded like any unusable
	// declaration rather than guessed from the union.
	bound, _, wait, err := bindPoolCatalog(ctx, in.pools, trigger)
	if err != nil && !isVerdict(err) {
		return nil, fmt.Errorf("resolve trigger %s's pool catalog: %w", trigger.ID, err)
	}
	// Which definition the event resolves is recorded on its row; a broken
	// one refuses it before any filter, so a broken file never reads as
	// "did not match".
	var (
		decl      *Declaration
		def       *core.TriggerEventDefinition
		workflows = opts.Batch.workflows(in.workflows)
	)
	switch {
	case err != nil:
	case wait != "":
		err = &DeclarationError{Workflow: trigger.Workflow, Name: triggerName(trigger), Reason: wait}
	default:
		trigger = bound
		if def, err = DefinitionOf(ctx, workflows, trigger); err != nil {
			return nil, err
		}
		decl, err = activationFor(ctx, PinnedTo(workflows, def), trigger)
	}
	// Who caused the event is resolved BEFORE any filter, because the filters
	// read it, and is frozen on the row so the history can explain a skip.
	origin := in.enrich(ctx, opts.Batch, trigger, &ev)
	switch {
	case def.Broken():
		outcome, detail = core.TriggerEventFailed, RefusalDetail(def)
	case err != nil && isVerdict(err):
		// Recorded, not dropped: "why did my trigger stop firing?" must have
		// an answer, and this is it.
		outcome, detail = core.TriggerEventFailed, err.Error()
	case err != nil:
		return nil, fmt.Errorf("resolve trigger %s's declaration: %w", trigger.ID, err)
	case len(SetupIssues(decl, trigger)) > 0:
		// An activation that was never finished (or whose workflow has since
		// grown a required input) cannot launch anything, and its filter reads
		// the very params that are missing, so evaluating it would only
		// report a misleading "did not match". Say what is wrong instead.
		// Recorded as skipped, not failed: the automation did not break, it is
		// waiting for its owner, and health reads that from the workflow.
		outcome, detail = core.TriggerEventSkipped, SetupDetail(SetupIssues(decl, trigger))
	case !opts.Manual:
		outcome, detail = in.applyOriginGuards(ctx, trigger, decl, ev, origin, occurredAt)
		if outcome == core.TriggerEventPending {
			filter := trigger.Filter
			if decl != nil {
				filter = decl.Filter
			}
			outcome, detail = in.applyFilter(ctx, filter, trigger, decl, ev, origin, occurredAt)
		}
	}

	dedupeAt := in.now()
	if opts.Receipt != nil {
		dedupeAt = opts.Receipt.ReceivedAt
	}
	dedupeKey := in.declaredDedupeKeyAt(ctx, trigger, decl, ev, occurredAt, dedupeAt)
	row := &core.TriggerEvent{
		ID:            uuid.NewString(),
		TriggerID:     &trigger.ID,
		UserID:        trigger.UserID,
		Kind:          ev.Kind,
		DedupeKey:     dedupeKey,
		OccurredAt:    occurredAt,
		Payload:       ev.Payload,
		Sender:        ev.Sender,
		Origin:        origin,
		Outcome:       outcome,
		OutcomeDetail: detail,
		Definition:    def,
		CreatedAt:     in.now().UTC(),
	}
	var created bool
	if opts.Receipt != nil {
		row, created, err = in.repo.RecordDeliveryTriggerEvent(ctx, opts.Receipt.ID, opts.Receipt.LeaseToken, row)
	} else {
		created, err = in.repo.CreateTriggerEvent(ctx, row)
		if err == nil && !created {
			row, err = in.repo.GetTriggerEventByDedupe(ctx, ev.Kind, dedupeKey)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("record %s event for trigger %s: %w", ev.Kind, trigger.ID, err)
	}
	if created && row.Outcome != core.TriggerEventPending {
		// Settled here, so no fire follows: this is its trigger.fire span. A
		// pending event's span is its fire's (EventFirer.Fire); a duplicate
		// delivery recorded nothing.
		traceSettled(ctx, trigger, row, row.Outcome, row.OutcomeDetail, trace.WithTimestamp(began))
	}
	return in.startRecordedEvent(ctx, trigger.ID, row, !created, opts.Manual), nil
}

func (in *Intake) startRecordedEvent(ctx context.Context, triggerID string, row *core.TriggerEvent, duplicate, manual bool) *AcceptResult {
	res := &AcceptResult{EventID: row.ID, Outcome: row.Outcome, Detail: row.OutcomeDetail, Duplicate: duplicate}
	if row.Outcome == core.TriggerEventPending {
		// A crash after the write is repaired by the redriver. Re-delivery
		// names the same event and does not create a second launch intent.
		input := EventFireInput{TriggerID: triggerID, Kind: row.Kind, DedupeKey: row.DedupeKey, Manual: manual}
		if err := StartEventFire(ctx, in.starter, row.ID, input, in.taskQueue); err != nil {
			logging.WarnContext(ctx, "could not start the fire of an accepted trigger event; the redriver will retry",
				"trigger_id", triggerID, "event_id", row.ID, "error", err)
		}
	}
	return res
}

func (in *Intake) applyFilter(ctx context.Context, expr string, trigger *core.Trigger, decl *Declaration, ev InboundEvent, origin *core.TriggerOrigin, occurredAt time.Time) (core.TriggerEventOutcome, string) {
	if strings.TrimSpace(expr) == "" {
		return core.TriggerEventPending, ""
	}
	filter, err := CompileFilter(expr)
	if err != nil {
		// Validated on write, so this is a filter stored before a rule
		// tightened. Record it; never fall back to firing.
		return core.TriggerEventFailed, "the trigger's filter does not compile: " + err.Error()
	}
	event := FilterInput{
		Kind: string(ev.Kind), TriggerID: trigger.ID, OccurredAt: occurredAt, Payload: ev.Payload, Sender: ev.Sender, Origin: origin,
	}
	scope := ActivationScope(ctx, in.repo, trigger, decl, event.Root())
	hit, err := filter.MatchScope(scope)
	if err != nil {
		return core.TriggerEventFailed, fmt.Sprintf(
			"the trigger's filter could not evaluate this event (%v); guard optional fields with has()", err)
	}
	if !hit {
		reason := "filter did not match: " + filter.Expr()
		if gap := OwnerIdentityGap(filter.Expr(), scope); gap != "" {
			reason = "filter did not match: " + gap + ". Filter: " + filter.Expr()
		}
		return core.TriggerEventSkipped, reason
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
//
// scope (MatchInput/MatchAny): for each scoped attribute the event's value
// must be one of the values the activation's input holds. A scope with no
// values matches nothing, and so does a scope whose projection is missing: an
// activation is never wider than what it declares.
func IntegrationEventMatches(cfg core.IntegrationConfig, eventType string, attrs map[string]string) bool {
	ok, _ := routeIntegrationEvent(cfg, eventType, attrs)
	return ok
}

// IntegrationScopeMiss names the scoped attribute that kept an event of a
// type cfg listens to away from it, or "" when the event matched or never
// reached the scope (wrong type, or a literal match failed). It exists so the
// router can say why a silent drop happened.
func IntegrationScopeMiss(cfg core.IntegrationConfig, eventType string, attrs map[string]string) string {
	if ok, miss := routeIntegrationEvent(cfg, eventType, attrs); !ok {
		return miss
	}
	return ""
}

func routeIntegrationEvent(cfg core.IntegrationConfig, eventType string, attrs map[string]string) (ok bool, scopeMiss string) {
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
		return false, ""
	}
	for key, want := range cfg.Match {
		got, ok := attrs[key]
		if !ok || got != want {
			return false, ""
		}
	}
	scoped := make([]string, 0, len(cfg.MatchInput))
	for attr := range cfg.MatchInput {
		scoped = append(scoped, attr)
	}
	sort.Strings(scoped)
	for _, attr := range scoped {
		got, ok := attrs[attr]
		if !ok || !slices.Contains(cfg.MatchAny[attr], got) {
			return false, attr
		}
	}
	return true, ""
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
