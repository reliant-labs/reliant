// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// Replay is a past trigger event to run a workflow against. The payload is
// whatever the person is replaying, edited or not.
type Replay struct {
	// TriggerName is the declared trigger whose inputs and prompt map the
	// event.
	TriggerName string
	// EventID is the recorded event being replayed; empty for a payload the
	// person wrote.
	EventID    string
	Kind       core.TriggerEventKind
	OccurredAt time.Time
	Payload    map[string]any
	Sender     *core.TriggerSender
}

// ReplayMapping is what the declared trigger turns a replayed event into: the
// inputs it maps and the prompt it renders.
type ReplayMapping struct {
	// Kind is the kind of event the declared trigger receives, which is what
	// the replayed event is recorded as when the replay does not say.
	Kind core.TriggerEventKind
	// Inputs are only the inputs the declaration maps from the event. The
	// run's other inputs are the person's to set.
	Inputs map[string]any
	// Prompt is the rendered prompt; empty when the declaration has none.
	Prompt string
}

// MapReplay maps a replayed event through the declaration of trigger
// replay.TriggerName in wf, exactly as a fire of that event would: the same
// scope, the same inputs evaluation, the same prompt rendering.
//
// Only the mapping applies. The declaration's filter and origin_filter are
// deliberately NOT evaluated: a person replaying an event has decided it is
// wanted, and the common reason to replay is an event the filter turned away.
//
// activation is the caller's own activation of that trigger, when they have
// one: its params and owner are what `trigger.activation` reads. Nil builds the
// scope from the workflow's input defaults alone, which is what a first-time
// activation would see.
func MapReplay(ctx context.Context, repo any, userID, workflowRef string, wf *reliantv1.Workflow, activation *core.Trigger, replay Replay) (*ReplayMapping, error) {
	name, err := replayTriggerName(wf, replay.TriggerName)
	if err != nil {
		return nil, err
	}
	bound := core.Trigger{UserID: userID, Workflow: workflowRef}
	if activation != nil {
		bound = *activation
	}
	// The declaration is read for the trigger the replay names, whatever the
	// caller's real activation is bound to.
	bound.WorkflowTrigger = &name
	bound.Kind = ""
	decl, err := DeclarationIn(wf, &bound)
	if err != nil {
		return nil, err
	}

	occurred := replay.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	kind := replay.Kind
	if kind == "" {
		kind = decl.Source.Kind.EventKind()
	}
	root := FilterInput{
		Kind:       string(kind),
		TriggerID:  bound.ID,
		EventID:    replay.EventID,
		OccurredAt: occurred,
		Payload:    replay.Payload,
		Sender:     replay.Sender,
	}.Root()
	scope := ActivationScope(ctx, repo, &bound, decl, root)

	out := &ReplayMapping{Kind: decl.Source.Kind.EventKind()}
	if len(decl.Inputs) > 0 {
		inputs, err := MergeDeclaredInputs(nil, decl.Inputs, scope)
		if err != nil {
			return nil, fmt.Errorf("the trigger %q could not read its inputs from this event: %w", name, err)
		}
		out.Inputs = inputs
	}
	prompt, err := SeedPrompt(&core.Trigger{}, decl, scope)
	if err != nil {
		return nil, err
	}
	out.Prompt = prompt
	return out, nil
}

// replayTriggerName picks the declared trigger a replay maps through: the one
// it names, or the workflow's only one when it names none.
func replayTriggerName(wf *reliantv1.Workflow, named string) (string, error) {
	named = strings.TrimSpace(named)
	declared := DeclaredTriggerNames(wf)
	if named != "" {
		if FindDeclaredTrigger(wf, named) == nil {
			return "", fmt.Errorf("the workflow declares no trigger named %q (it declares: %s)", named, strings.Join(declared, ", "))
		}
		return named, nil
	}
	switch len(declared) {
	case 0:
		return "", fmt.Errorf("the workflow declares no triggers, so there is no event to replay")
	case 1:
		return declared[0], nil
	default:
		return "", fmt.Errorf("the workflow declares several triggers (%s); name the one the event arrived through", strings.Join(declared, ", "))
	}
}

// SeedWithReplay appends a replayed event to prompt as the same delimited,
// labelled <trigger_event> block a real fire seeds its run with, so the agent
// reads a replay exactly as it would the original.
func SeedWithReplay(prompt string, replay Replay) string {
	occurred := replay.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	return seedWithEvent(prompt, &core.TriggerEvent{
		ID:         replay.EventID,
		Kind:       replay.Kind,
		OccurredAt: occurred,
		Payload:    replay.Payload,
	})
}

// ReplayKind is the kind of event a replay of trigger name is recorded as when
// the person did not choose one: the kind of event the declared trigger
// receives.
func ReplayKind(wf *reliantv1.Workflow, name string) (core.TriggerEventKind, error) {
	resolved, err := replayTriggerName(wf, name)
	if err != nil {
		return "", err
	}
	src, err := triggerspec.FromWorkflowTrigger(FindDeclaredTrigger(wf, resolved))
	if err != nil {
		return "", fmt.Errorf("the declaration of trigger %q is invalid: %w", resolved, err)
	}
	return src.Kind.EventKind(), nil
}

// ParseReplayKind reads the kind a client named for a replayed event. Only
// the kinds a trigger receives are replayable: a chat, an agent's run or
// another manual run is not an event a trigger declaration maps.
func ParseReplayKind(kind string) (core.TriggerEventKind, error) {
	switch k := core.TriggerEventKind(strings.TrimSpace(kind)); k {
	case core.TriggerEventKindSchedule, core.TriggerEventKindWebhook,
		core.TriggerEventKindIntegration, core.TriggerEventKindWorkflowEvent:
		return k, nil
	default:
		return "", fmt.Errorf("replay kind %q is not an event a trigger receives (schedule, webhook, integration or workflow_event)", kind)
	}
}

// ReplayFromProto reads a wire ReplayEvent and the original event's sender. An
// empty kind stays empty: the caller resolves it from the declaration
// (ReplayKind, or MapReplay).
func ReplayFromProto(r *reliantv1.ReplayEvent, sender *reliantv1.TriggerSender) (Replay, error) {
	out := Replay{
		TriggerName: strings.TrimSpace(r.GetTriggerName()),
		EventID:     strings.TrimSpace(r.GetEventId()),
		Payload:     map[string]any{},
	}
	if r.GetKind() != "" {
		kind, err := ParseReplayKind(r.GetKind())
		if err != nil {
			return Replay{}, err
		}
		out.Kind = kind
	}
	if raw := strings.TrimSpace(r.GetOccurredAt()); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return Replay{}, fmt.Errorf("replay occurred_at %q is not RFC3339", raw)
		}
		out.OccurredAt = at.UTC()
	}
	if r.GetPayload() != nil {
		out.Payload = r.GetPayload().AsMap()
	}
	if sender != nil {
		out.Sender = &core.TriggerSender{
			Kind:        core.TriggerSenderKind(sender.GetKind()),
			ID:          sender.GetId(),
			DisplayName: sender.GetDisplayName(),
			Verified:    sender.GetVerified(),
		}
	}
	return out, nil
}
