// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// An activation fires from its declaration, read from the workflow at fire
// time. These are the fire paths' halves of that contract; the declaration
// resolution itself is declaration_test.go.

func scheduleActivation(t *testing.T) *core.Trigger {
	t.Helper()
	name := "nightly"
	trig := testTrigger(t, nil)
	trig.Workflow = "triage"
	trig.WorkflowTrigger = &name
	trig.Params = map[string]any{"mode": "deep"}
	return trig
}

func TestScheduleActivationFiresFromItsDeclaration(t *testing.T) {
	repo := newFakeRepo()
	trig := scheduleActivation(t)
	repo.triggers[trig.ID] = trig
	launcher := &fakeLauncher{}

	workflows := fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}
	out, err := NewFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), fireReq(trig.ID))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	require.Len(t, launcher.snapshot(), 1)
	assert.Equal(t, "deep", launcher.snapshot()[0].Spec.Params["mode"].GetStringValue())
}

// A schedule's declared inputs read its slot.
func TestScheduleActivationEvaluatesDeclaredInputs(t *testing.T) {
	repo := newFakeRepo()
	trig := scheduleActivation(t)
	repo.triggers[trig.ID] = trig
	launcher := &fakeLauncher{}
	workflows := fakeWorkflows{yaml: map[string]string{"triage": `name: triage
inputs:
  report_date: {type: string, default: ""}
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    inputs:
      report_date: "{{ trigger.scheduled_for }}"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`}}
	_, err := NewFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), fireReq(trig.ID))
	require.NoError(t, err)
	require.Len(t, launcher.snapshot(), 1)
	params := launcher.snapshot()[0].Spec.Params
	assert.Equal(t, "2026-01-02T09:00:00Z", params["report_date"].GetStringValue())
	assert.Equal(t, "deep", params["mode"].GetStringValue(), "an unmapped param passes through")
}

// The declaration was renamed after activation: the fire records a failed
// firing with the reason and launches nothing. It does not fall back to the
// row's stored projection.
func TestScheduleActivationWithABrokenDeclarationFailsLoudly(t *testing.T) {
	repo := newFakeRepo()
	trig := scheduleActivation(t)
	repo.triggers[trig.ID] = trig
	launcher := &fakeLauncher{}

	workflows := fakeWorkflows{yaml: map[string]string{"triage": `name: triage
triggers:
  - name: morning
    schedule: {cron: "0 9 * * *"}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`}}
	_, err := NewFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), fireReq(trig.ID))
	require.Error(t, err, "a broken declaration ends the fire without retrying")
	assert.Empty(t, launcher.snapshot(), "nothing may launch from a declaration that no longer exists")

	events := repo.eventsFor(trig.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Contains(t, events[0].OutcomeDetail, `no longer declares a trigger named "nightly"`)
}

// A server that was not given a resolver cannot know what an activation
// declares, so it must not guess from the projection.
func TestActivationWithoutAResolverIsRefused(t *testing.T) {
	repo := newFakeRepo()
	trig := scheduleActivation(t)
	repo.triggers[trig.ID] = trig
	launcher := &fakeLauncher{}

	_, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trig.ID))
	require.Error(t, err)
	assert.Empty(t, launcher.snapshot())
}

// An inbound event for an activation: the filter and inputs are the
// declaration's, read at intake (filter) and launch (inputs) time, so an edit
// to the workflow takes effect on the next event without touching the row.
func TestIntegrationActivationFiltersAndMapsInputsFromItsDeclaration(t *testing.T) {
	repo := newFakeRepo()
	name := "new-issue"
	trig := &core.Trigger{
		ID: "t-int", UserID: "user-1", ProjectID: "project-1", Name: "my new issue",
		Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "triage", WorkflowTrigger: &name,
		Message: "Triage it.", DaemonID: testDaemonID,
		Config: json.RawMessage(`{"integration":"github","events":["issues.opened"]}`),
		// A stale projection: the declaration's filter is what decides.
		Filter: "false",
	}
	repo.triggers[trig.ID] = trig
	workflows := fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}

	starter := &recordingStarter{}
	intake := NewIntake(repo, starter, "q").WithWorkflows(workflows)
	payload := func(number int) map[string]any {
		return map[string]any{"data": map[string]any{"issue": map[string]any{"number": number}}}
	}

	// Filter miss (the declaration requires number > 0): skipped.
	res, err := intake.Accept(context.Background(), trig, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: "t-int:d0", Payload: payload(0), Sender: octocat,
	}, AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Contains(t, res.Detail, "trigger.payload.data.issue.number > 0")

	// Hit: pending, then launched with the event's issue number as an input.
	res, err = intake.Accept(context.Background(), trig, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: "t-int:d1", Payload: payload(42), Sender: octocat,
		OccurredAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
	}, AcceptOptions{})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)

	launcher := &fakeLauncher{}
	out, err := NewEventFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), EventFireInput{
		TriggerID: trig.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: "t-int:d1",
	})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	require.Len(t, launcher.snapshot(), 1)
	params := launcher.snapshot()[0].Spec.Params
	require.Contains(t, params, "issue_number")
	assert.Equal(t, float64(42), params["issue_number"].GetNumberValue())
}

// octocat is a GitHub receiver's verified sender.
var octocat = &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: "octocat", DisplayName: "octocat", Verified: true}

const senderGatedWorkflow = `name: triage
inputs:
  requester:
    type: string
    default: ""
triggers:
  - name: from-octocat
    integration:
      integration: github
      events: [issues.opened]
    filter: 'trigger.sender.id == "octocat"'
    inputs:
      requester: "{{ trigger.sender.display_name }}"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// A declared filter on trigger.sender gates intake, and the inputs mapping
// reads the same sender at launch.
func TestActivationFilterAndInputsReadTheSender(t *testing.T) {
	repo := newFakeRepo()
	name := "from-octocat"
	trig := &core.Trigger{
		ID: "t-sender", UserID: "user-1", ProjectID: "project-1", Name: "from octocat",
		Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "triage", WorkflowTrigger: &name,
		Message: "Triage it.", DaemonID: testDaemonID,
		Config: json.RawMessage(`{"integration":"github","events":["issues.opened"]}`),
	}
	repo.triggers[trig.ID] = trig
	workflows := fakeWorkflows{yaml: map[string]string{"triage": senderGatedWorkflow}}
	intake := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(workflows)

	hubot := &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: "hubot", DisplayName: "hubot", Verified: true}
	res, err := intake.Accept(context.Background(), trig, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: "t-sender:d0", Payload: map[string]any{}, Sender: hubot,
	}, AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome, "someone else's issue starts no run")

	res, err = intake.Accept(context.Background(), trig, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: "t-sender:d1", Payload: map[string]any{}, Sender: octocat,
	}, AcceptOptions{})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)

	launcher := &fakeLauncher{}
	_, err = NewEventFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), EventFireInput{
		TriggerID: trig.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: "t-sender:d1",
	})
	require.NoError(t, err)
	require.Len(t, launcher.snapshot(), 1)
	call := launcher.snapshot()[0]
	assert.Equal(t, octocat, call.Event.Sender)
	assert.Equal(t, "octocat", call.Spec.Params["requester"].GetStringValue())
}

// An event whose payload the declaration's inputs cannot read is recorded as
// failed with the input named, never launched with the input missing.
func TestActivationInputsThatCannotEvaluateFailTheFiring(t *testing.T) {
	repo := newFakeRepo()
	name := "new-issue"
	trig := &core.Trigger{
		ID: "t-int2", UserID: "user-1", ProjectID: "project-1", Name: "x",
		Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "triage", WorkflowTrigger: &name,
		Message: "Triage it.", DaemonID: testDaemonID,
		Config: json.RawMessage(`{"integration":"github","events":["issues.opened"]}`),
	}
	repo.triggers[trig.ID] = trig
	repo.events = append(repo.events, &core.TriggerEvent{
		ID: "ev1", TriggerID: &trig.ID, UserID: "user-1", Kind: core.TriggerEventKindIntegration,
		DedupeKey: "t-int2:d1", OccurredAt: time.Now(), Outcome: core.TriggerEventPending,
		// No issue.number: the filter would have missed it, but a manual
		// fire bypasses the filter and still meets the inputs mapping.
		Payload: map[string]any{"manual": true},
	})
	workflows := fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}
	launcher := &fakeLauncher{}
	_, err := NewEventFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), EventFireInput{
		TriggerID: trig.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: "t-int2:d1",
	})
	require.Error(t, err)
	assert.Empty(t, launcher.snapshot())
	events := repo.eventsFor(trig.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Contains(t, events[0].OutcomeDetail, "issue_number")
}

// The declaration's prompt template is what an activation with no message of
// its own starts from, rendered against the event. The event is still
// appended as untrusted data after it.
func TestIntegrationActivationSeedsFromTheDeclarationsPrompt(t *testing.T) {
	repo := newFakeRepo()
	name := "new-issue"
	trig := &core.Trigger{
		ID: "t-prompt", UserID: "user-1", ProjectID: "project-1", Name: "triage",
		Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "triage", WorkflowTrigger: &name,
		DaemonID: testDaemonID,
		Config:   json.RawMessage(`{"integration":"github","events":["issues.opened"]}`),
	}
	repo.triggers[trig.ID] = trig
	repo.events = append(repo.events, &core.TriggerEvent{
		ID: "ev-prompt", TriggerID: &trig.ID, UserID: "user-1", Kind: core.TriggerEventKindIntegration,
		DedupeKey: "t-prompt:d1", OccurredAt: time.Now(), Outcome: core.TriggerEventPending,
		Payload: map[string]any{"data": map[string]any{"issue": map[string]any{"number": 7}}},
	})
	workflows := fakeWorkflows{yaml: map[string]string{"triage": `name: triage
triggers:
  - name: new-issue
    integration: {integration: github, events: [issues.opened]}
    prompt: "Triage issue #{{ trigger.payload.data.issue.number }}."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`}}
	launcher := &fakeLauncher{}
	out, err := NewEventFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), EventFireInput{
		TriggerID: trig.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: "t-prompt:d1",
	})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	require.Len(t, launcher.snapshot(), 1)
	seed := launcher.snapshot()[0].Spec.Messages[0].Content
	assert.True(t, strings.HasPrefix(seed, "Triage issue #7.\n\n<trigger_event"), "seed: %q", seed)

	// An activation's own message overrides the template.
	trig.Message = "Just label it."
	repo.events = append(repo.events, &core.TriggerEvent{
		ID: "ev-prompt-2", TriggerID: &trig.ID, UserID: "user-1", Kind: core.TriggerEventKindIntegration,
		DedupeKey: "t-prompt:d2", OccurredAt: time.Now(), Outcome: core.TriggerEventPending,
		Payload: map[string]any{"data": map[string]any{"issue": map[string]any{"number": 8}}},
	})
	_, err = NewEventFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), EventFireInput{
		TriggerID: trig.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: "t-prompt:d2",
	})
	require.NoError(t, err)
	require.Len(t, launcher.snapshot(), 2)
	assert.True(t, strings.HasPrefix(launcher.snapshot()[1].Spec.Messages[0].Content, "Just label it.\n\n"))
}

// A schedule activation renders the template against its slot.
func TestScheduleActivationSeedsFromTheDeclarationsPrompt(t *testing.T) {
	repo := newFakeRepo()
	trig := scheduleActivation(t)
	trig.Message = ""
	repo.triggers[trig.ID] = trig
	launcher := &fakeLauncher{}
	workflows := fakeWorkflows{yaml: map[string]string{"triage": `name: triage
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "Summarise everything before {{ trigger.scheduled_for }}."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`}}
	_, err := NewFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), fireReq(trig.ID))
	require.NoError(t, err)
	require.Len(t, launcher.snapshot(), 1)
	assert.Equal(t, "Summarise everything before 2026-01-02T09:00:00Z.", launcher.snapshot()[0].Spec.Messages[0].Content)
}
