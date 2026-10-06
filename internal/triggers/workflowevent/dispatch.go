// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// chatIDNamespace seeds the deterministic chat id a workflow-event launch goes
// into: uuid.NewSHA1(namespace, dedupe key). A retried dispatch derives the
// same id, so the launcher's own idempotency catches a duplicate even if the
// event row and the launch were split by a crash.
//
// Do not change this value: dispatches retried across the change would
// launch twice.
var chatIDNamespace = uuid.MustParse("7d0f3a52-91c4-4b0e-a8f6-3c2e5b19d4a7")

// Repo is the slice of the repository dispatch reads and writes. The
// signatures match db.Repository, so *db.Repo satisfies it structurally.
type Repo interface {
	LaunchEventReader
	ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
}

// Launcher is the one door a run starts through. Implemented by
// *launch.Launcher.
type Launcher interface {
	Launch(ctx context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error)
}

// Spec is what matching needs from a trigger: its source and its CEL filter.
// Both a stored row and a workflow definition's `triggers:` block yield one.
type Spec struct {
	Source Source
	// Filter is a CEL bool over the `trigger` root the launched run would
	// see. Empty matches every event.
	Filter string
	// Inputs maps workflow inputs to templates over `trigger`; set for an
	// activation of a declared trigger.
	Inputs map[string]string
}

// SpecOf reads a stored trigger's workflow_event spec: Source from its config,
// filter from the row.
func SpecOf(t *core.Trigger) (Spec, error) {
	var src Source
	if len(t.Config) > 0 {
		if err := json.Unmarshal(t.Config, &src); err != nil {
			return Spec{}, fmt.Errorf("trigger %s: workflow_event config: %w", t.ID, err)
		}
	}
	return Spec{Source: src, Filter: triggerFilter(t)}, nil
}

// Dispatcher fires the workflow_event triggers a run event matches.
type Dispatcher struct {
	repo      Repo
	launcher  Launcher
	filter    FilterFunc
	workflows triggers.WorkflowResolver
	now       func() time.Time
}

// NewDispatcher builds a dispatcher. filter nil uses EvaluateFilter.
func NewDispatcher(repo Repo, launcher Launcher, filter FilterFunc) *Dispatcher {
	if filter == nil {
		filter = EvaluateFilter
	}
	return &Dispatcher{repo: repo, launcher: launcher, filter: filter, now: time.Now}
}

// WithWorkflows lets the dispatcher read an activation's declaration. Without
// it, an activation's matches are recorded as failed rather than matched
// against a projection that may be stale.
func (d *Dispatcher) WithWorkflows(workflows triggers.WorkflowResolver) *Dispatcher {
	d.workflows = workflows
	return d
}

// specOf is a trigger's effective spec: an activation's from its workflow's
// declaration, read now; an ad hoc trigger's from its row.
func (d *Dispatcher) specOf(ctx context.Context, t *core.Trigger) (Spec, error) {
	if t.WorkflowTrigger == nil || *t.WorkflowTrigger == "" {
		return SpecOf(t)
	}
	if d.workflows == nil {
		return Spec{}, &triggers.DeclarationError{Workflow: t.Workflow, Name: *t.WorkflowTrigger,
			Reason: "this server cannot read declared triggers"}
	}
	decl, err := triggers.ResolveDeclaration(ctx, d.workflows, t)
	if err != nil {
		return Spec{}, err
	}
	var src Source
	if err := json.Unmarshal(decl.Source.Config, &src); err != nil {
		return Spec{}, fmt.Errorf("trigger %s: declared workflow_event config: %w", t.ID, err)
	}
	return Spec{Source: src, Filter: decl.Filter, Inputs: decl.Inputs}, nil
}

// Result is one trigger's verdict on one run event.
type Result struct {
	TriggerID string
	Outcome   core.TriggerEventOutcome
	Detail    string
	ChatID    string
}

// Dispatch matches ev against its owner's enabled workflow_event triggers and
// launches a run for each match. It is idempotent: every launch and every
// recorded skip is keyed by (trigger, run event), so a retry repeats nothing.
//
// The error is non-nil only for failures worth retrying (a database blip, the
// launcher's internal errors). A trigger that can never launch as configured
// is recorded as a failed event and does not fail the dispatch, so one broken
// trigger cannot block the others.
func (d *Dispatcher) Dispatch(ctx context.Context, ev *core.RunEvent) ([]Result, error) {
	// Same owner only. The listing is scoped by the EVENT's owner, so another
	// user's trigger is never even considered.
	candidates, err := d.repo.ListTriggers(ctx, core.TriggerFilters{UserID: ev.UserID})
	if err != nil {
		return nil, fmt.Errorf("list triggers of %s: %w", ev.UserID, err)
	}

	var (
		lineage       Lineage
		lineageLoaded bool
		results       []Result
		retryable     []error
	)
	for _, trigger := range candidates {
		if trigger.Kind != runevents.TriggerKind || !trigger.Enabled || trigger.UserID != ev.UserID {
			continue
		}
		spec, err := d.specOf(ctx, trigger)
		if err != nil {
			var declErr *triggers.DeclarationError
			switch {
			case errors.As(err, &declErr):
				// A broken activation is recorded on every event it would
				// have matched under its last projection, so the owner
				// sees why it went quiet; it never fires.
				if prev, prevErr := SpecOf(trigger); prevErr == nil && !prev.Source.Matches(ev.WorkflowName, ev.Outcome) {
					continue
				}
				res, recErr := d.record(ctx, trigger, ev, nil, core.TriggerEventFailed, declErr.Error())
				results, retryable = appendResult(results, retryable, res, recErr)
			case trigger.WorkflowTrigger != nil:
				// The workflow could not be READ: retry the dispatch.
				retryable = append(retryable, fmt.Errorf("resolve trigger %s: %w", trigger.ID, err))
			default:
				res, recErr := d.record(ctx, trigger, ev, nil, core.TriggerEventFailed, "trigger config is invalid: "+err.Error())
				results, retryable = appendResult(results, retryable, res, recErr)
			}
			continue
		}
		// Kind and workflow mismatches are filtered BEFORE any row is written:
		// an hourly run of an unrelated workflow is not an event this trigger
		// saw, and recording it would bury the ones it did.
		if !spec.Source.Matches(ev.WorkflowName, ev.Outcome) {
			continue
		}

		if !lineageLoaded {
			if lineage, err = LineageOf(ctx, d.repo, ev.ChatID); err != nil {
				return results, err
			}
			lineageLoaded = true
		}
		res, err := d.fire(ctx, trigger, spec, ev, lineage)
		results, retryable = appendResult(results, retryable, res, err)
	}
	return results, errors.Join(retryable...)
}

func appendResult(results []Result, errs []error, res *Result, err error) ([]Result, []error) {
	if res != nil {
		results = append(results, *res)
	}
	if err != nil {
		errs = append(errs, err)
	}
	return results, errs
}

// fire applies the loop guards and the filter to one matching trigger, then
// launches.
func (d *Dispatcher) fire(ctx context.Context, trigger *core.Trigger, spec Spec, ev *core.RunEvent, lineage Lineage) (*Result, error) {
	// A trigger never fires on a run it launched, nor on any descendant of one.
	if lineage.Contains(trigger.ID) {
		return d.record(ctx, trigger, ev, lineage, core.TriggerEventSkipped,
			"loop guard: this run descends from a run this trigger launched")
	}
	if len(lineage) >= MaxChainDepth {
		return d.record(ctx, trigger, ev, lineage, core.TriggerEventSkipped,
			fmt.Sprintf("loop guard: launch chain is already %d workflow events deep (max %d)", len(lineage), MaxChainDepth))
	}

	next := lineage.Extend(trigger.ID, ev.ChatID)
	launchEv := d.buildEvent(trigger, ev, next)

	// The filter sees exactly what the launched run would see as `trigger`.
	pass, err := d.filter(spec.Filter, triggerRoot(launchEv))
	if err != nil {
		return d.record(ctx, trigger, ev, lineage, core.TriggerEventFailed, "filter error: "+err.Error())
	}
	if !pass {
		return d.record(ctx, trigger, ev, lineage, core.TriggerEventSkipped, "filter did not match")
	}

	launchSpec, err := d.buildSpec(trigger, ev, launchEv, spec.Inputs)
	if err != nil {
		return d.record(ctx, trigger, ev, lineage, core.TriggerEventFailed, err.Error())
	}

	res, launchErr := d.launcher.Launch(ctx, launchEv, launchSpec)
	switch {
	case launchErr == nil:
		return &Result{TriggerID: trigger.ID, Outcome: core.TriggerEventLaunched, ChatID: res.Chat.ID}, nil
	case errors.Is(launchErr, launch.ErrAlreadyLaunched):
		out := &Result{TriggerID: trigger.ID, Outcome: core.TriggerEventLaunched, Detail: "already launched"}
		var already *launch.AlreadyLaunchedError
		if errors.As(launchErr, &already) {
			out.ChatID = already.ChatID
		}
		return out, nil
	}

	var validation *launch.ValidationError
	if errors.As(launchErr, &validation) || errors.Is(launchErr, launch.ErrNotFound) {
		// Retrying cannot fix a spec that can never launch; record why.
		return d.record(ctx, trigger, ev, lineage, core.TriggerEventFailed, launchErr.Error())
	}
	return nil, fmt.Errorf("launch trigger %s for run event %s: %w", trigger.ID, ev.ID, launchErr)
}

// dedupeKey identifies one (trigger, run event) firing.
func dedupeKey(triggerID, runEventID string) string { return triggerID + ":" + runEventID }

// buildEvent is the trigger_events row a launch records: the source run's
// description under payload, plus the lineage the loop guard reads.
func (d *Dispatcher) buildEvent(trigger *core.Trigger, ev *core.RunEvent, lineage Lineage) launch.Event {
	payload := make(map[string]any, len(ev.Payload)+3)
	for key, value := range ev.Payload {
		payload[key] = value
	}
	payload["trigger_name"] = trigger.Name
	payload["run_event_id"] = ev.ID
	payload[payloadLineage] = lineage.toPayload()
	return launch.Event{
		Kind:       runevents.TriggerEventKind,
		TriggerID:  trigger.ID,
		DedupeKey:  dedupeKey(trigger.ID, ev.ID),
		OccurredAt: ev.OccurredAt,
		Payload:    payload,
	}
}

// triggerRoot is the `trigger` CEL value a run launched from ev sees, built
// the same way the runtime builds it (runtime.TriggerInfo.CELValue).
func triggerRoot(ev launch.Event) map[string]any {
	name, _ := ev.Payload["trigger_name"].(string)
	occurred := ev.OccurredAt.UTC().Format(time.RFC3339)
	return map[string]any{
		"kind":          string(ev.Kind),
		"trigger_id":    ev.TriggerID,
		"event_id":      "",
		"occurred_at":   occurred,
		"scheduled_for": occurred,
		"name":          name,
		"payload":       ev.Payload,
	}
}

// buildSpec is what a workflow-event run is: owned by the trigger's user,
// unattended, seeded with the trigger's prompt plus a hidden note saying what
// started it.
func (d *Dispatcher) buildSpec(trigger *core.Trigger, ev *core.RunEvent, launchEv launch.Event, inputs map[string]string) (launch.Spec, error) {
	dedupe := launchEv.DedupeKey
	values, err := triggers.MergeDeclaredInputs(trigger.Params, inputs, triggerRoot(launchEv))
	if err != nil {
		return launch.Spec{}, fmt.Errorf("the declared trigger's inputs could not be read from this event: %w", err)
	}
	params, err := paramsToProto(values)
	if err != nil {
		return launch.Spec{}, fmt.Errorf("trigger params are not representable: %w", err)
	}
	source := ev.WorkflowName
	if source == "" {
		source = "a workflow"
	}
	title := fmt.Sprintf("%s · %s %s", trigger.Name, source, ev.Outcome)
	hidden := reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN
	return launch.Spec{
		OwnerUserID: trigger.UserID,
		ProjectID:   trigger.ProjectID,
		WorktreeID:  trigger.WorktreeID,
		NewChatID:   uuid.NewSHA1(chatIDNamespace, []byte(dedupe)).String(),
		Title:       &title,
		Workflow:    trigger.Workflow,
		DaemonID:    trigger.DaemonID,
		NoMachine:   trigger.NoMachine,
		Presets:     trigger.Presets,
		Params:      params,
		Messages: []launch.SeedMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: trigger.Message},
			{
				// The agent cannot infer from inputs it never sees that it is
				// unattended or why it started. The source run's own text is
				// NOT inlined here: it is untrusted data and stays under
				// trigger.payload, where a workflow chooses how to use it.
				Role: reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM,
				Content: fmt.Sprintf(
					"This run was started automatically by the trigger %q because run %s of %s reached %q. "+
						"No human is watching: nobody will answer a question or approve a request, "+
						"so decide and proceed, and record anything that needs review in your final response.",
					trigger.Name, ev.ChatID, source, string(ev.Outcome)),
				DisplayStyle: &hidden,
			},
		},
		Unattended:      true,
		GenerateTitle:   false,
		GreenfieldProbe: false,
	}, nil
}

// record writes the trigger_events row for a firing that did NOT launch.
// Launched events are written by the launcher in the chat's transaction.
func (d *Dispatcher) record(
	ctx context.Context,
	trigger *core.Trigger,
	ev *core.RunEvent,
	lineage Lineage,
	outcome core.TriggerEventOutcome,
	detail string,
) (*Result, error) {
	launchEv := d.buildEvent(trigger, ev, lineage)
	row := &core.TriggerEvent{
		ID:            uuid.NewString(),
		TriggerID:     &trigger.ID,
		UserID:        trigger.UserID,
		Kind:          runevents.TriggerEventKind,
		DedupeKey:     launchEv.DedupeKey,
		OccurredAt:    ev.OccurredAt,
		Payload:       launchEv.Payload,
		Outcome:       outcome,
		OutcomeDetail: detail,
		CreatedAt:     d.now().UTC(),
	}
	created, err := d.repo.CreateTriggerEvent(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("record %s event for trigger %s: %w", outcome, trigger.ID, err)
	}
	if !created {
		// A retry, or a concurrent dispatch, already recorded this firing.
		// The stored row is the verdict.
		stored, err := d.repo.GetTriggerEventByDedupe(ctx, runevents.TriggerEventKind, row.DedupeKey)
		if err != nil {
			return nil, fmt.Errorf("load existing event %s: %w", row.DedupeKey, err)
		}
		out := &Result{TriggerID: trigger.ID, Outcome: stored.Outcome, Detail: stored.OutcomeDetail}
		if stored.ChatID != nil {
			out.ChatID = *stored.ChatID
		}
		return out, nil
	}
	if outcome != core.TriggerEventSkipped {
		logging.Warn("[workflowevent] trigger did not launch",
			"triggerID", trigger.ID, "runEventID", ev.ID, "outcome", outcome, "detail", detail)
	}
	return &Result{TriggerID: trigger.ID, Outcome: outcome, Detail: detail}, nil
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

// Validate checks and canonicalizes a source at write time: every outcome is
// one of finished, failed, blocked (deduplicated, lowercased), and blank
// workflow refs are dropped.
func (s Source) Validate() (Source, error) {
	out := Source{}
	seen := map[string]bool{}
	for _, o := range s.Outcomes {
		o = strings.ToLower(strings.TrimSpace(o))
		if !ValidOutcome(o) {
			return Source{}, fmt.Errorf("outcome %q is not one of finished, failed, blocked", o)
		}
		if !seen[o] {
			seen[o] = true
			out.Outcomes = append(out.Outcomes, o)
		}
	}
	for _, ref := range s.Workflows {
		if ref = strings.TrimSpace(ref); ref != "" {
			out.Workflows = append(out.Workflows, ref)
		}
	}
	return out, nil
}
