// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// payloadLineage is the trigger-event payload key a workflow-event launch
// records its lineage under. It lives on the LAUNCHED run's trigger event, so
// a later event from that run can read where it came from without walking.
const payloadLineage = "lineage"

// Link is one workflow-event hop: TriggerID fired because SourceRunID reached
// an outcome.
type Link struct {
	TriggerID   string `json:"trigger_id"`
	SourceRunID string `json:"source_run_id"`
}

// Lineage is the chain of workflow-event links that led to a run, root first.
// A run nobody started through a workflow event has an empty lineage.
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

// toPayload renders the lineage as a JSON-friendly value for an event row.
func (l Lineage) toPayload() []any {
	out := make([]any, 0, len(l))
	for _, link := range l {
		out = append(out, map[string]any{"trigger_id": link.TriggerID, "source_run_id": link.SourceRunID})
	}
	return out
}

// lineageFromPayload reads a lineage back from an event payload. A payload
// without one is an empty lineage; one that is present but malformed is an
// error, because guessing would disable the loop guard.
func lineageFromPayload(payload map[string]any) (Lineage, error) {
	raw, ok := payload[payloadLineage]
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

// LineageOf returns the lineage of the run in chatID: the lineage recorded on
// its launch event when a workflow event launched it, else empty.
//
// Lineage is inherited at launch time, so this is one indexed read, not a
// walk — and a corrupt or cyclic history cannot make it loop.
func LineageOf(ctx context.Context, reader LaunchEventReader, chatID string) (Lineage, error) {
	ev, err := reader.GetTriggerEventByChatID(ctx, chatID)
	if errors.Is(err, core.ErrTriggerEventNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load launch event of %s: %w", chatID, err)
	}
	if ev == nil || ev.Kind != runevents.TriggerEventKind {
		return nil, nil
	}
	return lineageFromPayload(ev.Payload)
}
