// Copyright (c) 2025 Reliant Labs

// Package lineage is the loop guard every event-driven launch shares: a run
// launched by an event records the chain of (trigger, source run) hops that led
// to it, and a trigger neither re-fires on its own descendants nor lets a chain
// grow past MaxDepth.
//
// A workflow_event hop is "this trigger fired because that run reached an
// outcome"; an integration hop is "this trigger fired because that run wrote the
// thing the event reports" (trigger.origin). Both extend the same chain, so a
// workflow that reacts to its own writes through GitHub is stopped by the same
// rule that stops one reacting to its own outcome.
//
// MaxDepth is a HARD guard. trigger.origin_filter decides which echoes a
// workflow wants, and may say "all of them"; no expression reaches this cap, so
// a pair of workflows that deliberately re-trigger each other still stops.
//
//forge:exclude-contract: pure value type and functions over core types, shared by the workflow-event dispatcher and the integration intake, which must not import each other; no state, no I/O, no substitution point
package lineage

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// MaxDepth caps how many hops may precede a launch. A run started by a human
// has depth 0; the run its trigger starts has depth 1; and so on. A launch that
// would sit deeper is refused.
const MaxDepth = 5

// PayloadKey is the trigger-event payload key a workflow-event launch records
// its lineage under. It lives on the LAUNCHED run's trigger event, so a later
// event from that run can read where it came from without walking.
const PayloadKey = "lineage"

// Link is one hop: TriggerID fired because of something SourceRunID did (it
// reached an outcome, or it wrote what an integration event reports).
type Link struct {
	TriggerID   string `json:"trigger_id"`
	SourceRunID string `json:"source_run_id"`
}

// Lineage is the chain of links that led to a run, root first. A run nobody
// started through an event has an empty lineage.
type Lineage []Link

// Contains reports whether triggerID launched this run or one of its
// ancestors — i.e. whether firing it again would loop.
func (l Lineage) Contains(triggerID string) bool {
	for _, link := range l {
		if link.TriggerID == triggerID {
			return true
		}
	}
	return false
}

// Extend is the lineage of a run triggerID launches because sourceRunID
// reached an outcome. The receiver is not mutated.
func (l Lineage) Extend(triggerID, sourceRunID string) Lineage {
	out := make(Lineage, 0, len(l)+1)
	out = append(out, l...)
	return append(out, Link{TriggerID: triggerID, SourceRunID: sourceRunID})
}

// ToPayload renders the lineage as a JSON-friendly value for an event row.
func (l Lineage) ToPayload() []any {
	out := make([]any, 0, len(l))
	for _, link := range l {
		out = append(out, map[string]any{"trigger_id": link.TriggerID, "source_run_id": link.SourceRunID})
	}
	return out
}

// FromPayload reads a lineage back from an event payload. A payload
// without one is an empty lineage; one that is present but malformed is an
// error, because guessing would disable the loop guard.
func FromPayload(payload map[string]any) (Lineage, error) {
	raw, ok := payload[PayloadKey]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("lineage is %T, not a list", raw)
	}
	out := make(Lineage, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("lineage[%d] is %T, not an object", i, item)
		}
		triggerID, _ := m["trigger_id"].(string)
		sourceRunID, _ := m["source_run_id"].(string)
		if triggerID == "" {
			return nil, fmt.Errorf("lineage[%d] has no trigger_id", i)
		}
		out = append(out, Link{TriggerID: triggerID, SourceRunID: sourceRunID})
	}
	return out, nil
}

// LaunchEventReader finds the event that launched a chat. Satisfied by
// *db.Repo.
type LaunchEventReader interface {
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
}

// Of returns the lineage of the run in chatID: the lineage recorded on its
// launch event when an inbound event (a workflow event or an integration event)
// launched it, else empty.
//
// Lineage is inherited at launch time, so this is one indexed read, not a
// walk — and a corrupt or cyclic history cannot make it loop.
func Of(ctx context.Context, reader LaunchEventReader, chatID string) (Lineage, error) {
	ev, err := reader.GetTriggerEventByChatID(ctx, chatID)
	if errors.Is(err, core.ErrTriggerEventNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load launch event of %s: %w", chatID, err)
	}
	if ev == nil {
		return nil, nil
	}
	switch ev.Kind {
	case core.TriggerEventKindWorkflowEvent:
		return FromPayload(ev.Payload)
	case core.TriggerEventKindIntegration:
		return fromOrigin(ev), nil
	}
	return nil, nil
}

// Blocked says whether a workflow_event trigger firing triggerID from a run with
// this lineage must be refused, and why:
//
//   - a trigger never fires on a run it launched, nor on any descendant of one;
//   - a chain never grows past MaxDepth.
func (l Lineage) Blocked(triggerID string) (reason string, blocked bool) {
	if l.Contains(triggerID) {
		return "loop guard: this run descends from a run this trigger launched", true
	}
	return l.AtCap()
}

// AtCap says whether a chain of this depth may grow no further. It is the only
// guard an integration-event launch is held to: whether a workflow wants its own
// or another workflow's echoes is the author's call (origin_filter), and a pair
// of workflows may deliberately re-trigger each other, but never past MaxDepth,
// and no expression reaches this.
func (l Lineage) AtCap() (reason string, capped bool) {
	if len(l) >= MaxDepth {
		return fmt.Sprintf("loop guard: launch chain is already %d hops deep (max %d)", len(l), MaxDepth), true
	}
	return "", false
}

// Depth is the number of hops that led to a run.
func (l Lineage) Depth() int { return len(l) }

// ToCore renders the lineage as the links trigger.origin carries.
func (l Lineage) ToCore() []core.OriginLink {
	if len(l) == 0 {
		return nil
	}
	out := make([]core.OriginLink, 0, len(l))
	for _, link := range l {
		out = append(out, core.OriginLink{TriggerID: link.TriggerID, SourceRunID: link.SourceRunID})
	}
	return out
}

// FromCore reads a lineage back from trigger.origin's links.
func FromCore(links []core.OriginLink) Lineage {
	if len(links) == 0 {
		return nil
	}
	out := make(Lineage, 0, len(links))
	for _, link := range links {
		out = append(out, Link{TriggerID: link.TriggerID, SourceRunID: link.SourceRunID})
	}
	return out
}

// anonymous stands for a hop whose ids are not ours to know (another user's
// run). It matches no trigger, and still counts toward the cap.
const anonymous = "?"

// fromOrigin is the lineage of the run an integration event launched: the chain
// that led to the run that wrote (trigger.origin), plus this hop. An event no
// run of ours caused starts a chain of its own (empty).
func fromOrigin(ev *core.TriggerEvent) Lineage {
	o := ev.Origin
	if o == nil || !o.CausedByReliant || ev.TriggerID == nil {
		return nil
	}
	chain := FromCore(o.Lineage)
	for len(chain) < o.Depth {
		chain = append(Lineage{{TriggerID: anonymous}}, chain...)
	}
	return chain.Extend(*ev.TriggerID, o.RunID)
}
