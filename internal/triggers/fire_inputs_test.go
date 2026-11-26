// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

const fireInputsWorkflow = `name: alerts
inputs:
  action_id: {type: string, default: ""}
  mode: {type: string, default: ""}
triggers:
  - name: approved
    webhook: {}
    dedupe_key: "{{ trigger.payload.body.groupKey }}"
    dedupe_window: 30m
    inputs:
      mode: "fixed"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

var userSender = func(id string) *core.TriggerSender {
	return &core.TriggerSender{Kind: core.TriggerSenderKindUser, ID: id, Verified: true}
}

func manualFire(trigger *core.Trigger, dedupeKey string, inputs map[string]any) InboundEvent {
	payload := map[string]any{"manual": true, "requested_by": "user-1"}
	if inputs != nil {
		payload[PayloadInputs] = inputs
	}
	return InboundEvent{
		Kind: core.TriggerEventKindWebhook, DedupeKey: dedupeKey, Payload: payload, Sender: userSender("user-1"),
	}
}

func approvedActivation() *core.Trigger {
	return alertActivation("approved")
}

// A manual fire's inputs reach the run as workflow params, next to the
// activation's own, and a name the declaration maps from each event stays the
// event's.
func TestManualFireInputsReachTheRun(t *testing.T) {
	repo := newFakeRepo()
	trig := approvedActivation()
	trig.Workflow = "alerts"
	repo.triggers[trig.ID] = trig
	workflows := fakeWorkflows{yaml: map[string]string{"alerts": fireInputsWorkflow}}
	intake := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(workflows)

	res, err := intake.Accept(context.Background(), trig,
		manualFire(trig, trig.ID+":manual-1", map[string]any{"action_id": "act-1", "mode": "attacker"}),
		AcceptOptions{Manual: true})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)

	launcher := &fakeLauncher{}
	_, err = NewEventFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(),
		EventFireInput{TriggerID: trig.ID, Kind: core.TriggerEventKindWebhook, DedupeKey: trig.ID + ":manual-1", Manual: true})
	require.NoError(t, err)
	calls := launcher.snapshot()
	require.Len(t, calls, 1)
	params := calls[0].Spec.Params
	assert.Equal(t, "act-1", params["action_id"].GetStringValue())
	assert.Equal(t, "fixed", params["mode"].GetStringValue(), "an input the trigger maps from the event is not the caller's")
}

// A payload that only looks like a manual fire's, from a delivery, supplies
// no inputs: they come from the signed-in caller or nowhere.
func TestDeliveryPayloadCannotSupplyFireInputs(t *testing.T) {
	assert.Nil(t, FireInputsOf(map[string]any{"manual": true, PayloadInputs: map[string]any{"a": 1}},
		&core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: "t"}))
	assert.Nil(t, FireInputsOf(map[string]any{PayloadInputs: map[string]any{"a": 1}}, userSender("u")))
	assert.Nil(t, FireInputsOf(map[string]any{"manual": true, PayloadInputs: map[string]any{"a": 1}}, nil))
	assert.Equal(t, map[string]any{"a": 1}, FireInputsOf(map[string]any{"manual": true, PayloadInputs: map[string]any{"a": 1}}, userSender("u")))
}

// A caller's idempotency key is the event's identity: a repeat is the
// intake's duplicate and returns the first event and chat, even when the
// trigger declares a dedupe_key that would otherwise rewrite it, and a
// repeat carrying other inputs starts nothing.
func TestExplicitDedupeKeyReturnsTheFirstEvent(t *testing.T) {
	repo := newFakeRepo()
	trig := approvedActivation()
	trig.Workflow = "alerts"
	repo.triggers[trig.ID] = trig
	workflows := fakeWorkflows{yaml: map[string]string{"alerts": fireInputsWorkflow}}
	starter := &recordingStarter{}
	intake := NewIntake(repo, starter, "q").WithWorkflows(workflows)
	ctx := context.Background()
	key := trig.ID + ":idem:approve-1"

	first, err := intake.Accept(ctx, trig, manualFire(trig, key, map[string]any{"action_id": "act-1"}),
		AcceptOptions{Manual: true, ExplicitDedupeKey: true})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventPending, first.Outcome, first.Detail)
	assert.False(t, first.Duplicate)
	assert.Equal(t, EventChatID(core.TriggerEventKindWebhook, key), first.ChatID)

	replay, err := intake.Accept(ctx, trig, manualFire(trig, key, map[string]any{"action_id": "act-2"}),
		AcceptOptions{Manual: true, ExplicitDedupeKey: true})
	require.NoError(t, err)
	assert.True(t, replay.Duplicate)
	assert.Equal(t, first.EventID, replay.EventID)
	assert.Equal(t, first.ChatID, replay.ChatID)

	require.Len(t, repo.eventsFor(trig.ID), 1, "one row")
	assert.Equal(t, map[string]any{"action_id": "act-1"}, repo.eventsFor(trig.ID)[0].Payload[PayloadInputs],
		"the first request's inputs are the ones recorded")
	assert.Equal(t, key, repo.eventsFor(trig.ID)[0].DedupeKey, "the declared dedupe_key did not rewrite it")
	assert.Len(t, starter.snapshot(), 2, "the repeat names the same fire workflow id, which Temporal deduplicates")
	assert.Equal(t, starter.snapshot()[0].ID, starter.snapshot()[1].ID)

	// Another trigger's identical key is its own event.
	other := approvedActivation()
	other.ID = "t-other"
	other.Workflow = "alerts"
	repo.triggers[other.ID] = other
	otherKey := other.ID + ":idem:approve-1"
	got, err := intake.Accept(ctx, other, manualFire(other, otherKey, map[string]any{"action_id": "act-1"}),
		AcceptOptions{Manual: true, ExplicitDedupeKey: true})
	require.NoError(t, err)
	assert.False(t, got.Duplicate)
	assert.NotEqual(t, first.EventID, got.EventID)
	assert.NotEqual(t, first.ChatID, got.ChatID)
}

// Inputs the caller supplies count toward what the activation needs, so a
// required input left unset by the activation is not "needs setup" when the
// firing provides it.
func TestManualFireInputsSatisfyARequiredInput(t *testing.T) {
	const required = `name: alerts
inputs:
  action_id: {type: string, required: true}
triggers:
  - name: approved
    webhook: {}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	repo := newFakeRepo()
	trig := approvedActivation()
	trig.Workflow = "alerts"
	repo.triggers[trig.ID] = trig
	intake := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(fakeWorkflows{yaml: map[string]string{"alerts": required}})
	ctx := context.Background()

	without, err := intake.Accept(ctx, trig, manualFire(trig, trig.ID+":manual-a", nil), AcceptOptions{Manual: true})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, without.Outcome, "still refused with nothing supplied")

	with, err := intake.Accept(ctx, trig, manualFire(trig, trig.ID+":manual-b", map[string]any{"action_id": "x"}), AcceptOptions{Manual: true})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, with.Outcome, with.Detail)
}
