// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// An activation whose params leave a required input unset is NEEDS_SETUP, read
// from the workflow as it is now: no row is migrated when a workflow grows a
// required input, and an old `{}` activation of one shows up on its own.
const requiredReposWorkflow = `name: triage
inputs:
  repos:
    type: array
  team:
    type: string
    default: platform
  issue_number:
    type: integer
    default: 0
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.attributes.repository in trigger.activation.params.repos"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "triage #{{ trigger.payload.data.issue.number }}"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func requiredReposDecl(t *testing.T, params map[string]any) (*Declaration, *core.Trigger) {
	t.Helper()
	trig := repoActivation("act", params)
	decl, err := ResolveDeclaration(context.Background(),
		fakeWorkflows{yaml: map[string]string{"triage": requiredReposWorkflow}}, trig)
	require.NoError(t, err)
	return decl, trig
}

func TestNeedsSetupHealthNamesTheMissingInput(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"nil params":        nil,
		"empty params {}":   {},
		"a null value":      {"repos": nil},
		"another param set": {"team": "frontend"},
	} {
		t.Run(name, func(t *testing.T) {
			decl, trig := requiredReposDecl(t, params)
			health := NeedsSetupHealth(decl, trig)
			require.NotNil(t, health)
			assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, health.Status)
			assert.Equal(t, "needs setup: repos is required", health.LastFailureDetail)
			require.Len(t, health.SetupIssues, 1)
			assert.Equal(t, "repos", health.SetupIssues[0].Input)
			assert.Equal(t, "repos is required", health.SetupIssues[0].Message)
		})
	}
}

func TestNeedsSetupHealthIsNilForACompleteActivation(t *testing.T) {
	decl, trig := requiredReposDecl(t, map[string]any{"repos": []any{"acme/web"}})
	assert.Nil(t, NeedsSetupHealth(decl, trig))

	// The event-mapped input is the trigger's to fill, not the activation's.
	assert.Empty(t, SetupIssues(decl, trig))
}

func TestNeedsSetupHealthFlagsAValueThatNoLongerFitsAndNeverAnAdHocTrigger(t *testing.T) {
	decl, trig := requiredReposDecl(t, map[string]any{"repos": "acme/web"})
	health := NeedsSetupHealth(decl, trig)
	require.NotNil(t, health, "a stored value of the wrong shape is a setup issue too")
	assert.Equal(t, "repos", health.SetupIssues[0].Input)

	assert.Nil(t, NeedsSetupHealth(nil, testTrigger(t, nil)), "an ad hoc trigger has no workflow inputs to satisfy")
}

// Precedence: NEEDS_SETUP replaces whatever the firings said, including a
// FAILING streak, because nothing launches until the activation is finished;
// the rule that BROKEN outranks it lives where BROKEN is decided (a broken
// declaration never resolves, so there is no declaration to ask).
func TestNeedsSetupHealthOutranksFiringDerivedStatus(t *testing.T) {
	failing := ComputeHealth([]*core.TriggerEventWithRun{
		outcomeOnly(core.TriggerEventFailed, "boom"), outcomeOnly(core.TriggerEventFailed, "boom"),
	})
	require.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, failing.Status)

	decl, trig := requiredReposDecl(t, nil)
	health := NeedsSetupHealth(decl, trig)
	require.NotNil(t, health)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP, health.Status)
	assert.Zero(t, health.ConsecutiveFailures, "it is a replacement, not a blend")
}

func TestIntakeRecordsASkipWithTheSetupDetailInsteadOfEvaluatingTheFilter(t *testing.T) {
	repo := newFakeRepo()
	trig := repoActivation("act", map[string]any{}) // the owner's existing `{}` activation
	repo.triggers[trig.ID] = trig
	starter := &recordingStarter{}
	intake := NewIntake(repo, starter, "q").WithWorkflows(fakeWorkflows{yaml: map[string]string{"triage": requiredReposWorkflow}})

	res, err := intake.Accept(context.Background(), trig, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: trig.ID + ":e1",
		OccurredAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
		Payload:    issueIn("acme/web", 7), Sender: octocat,
	}, AcceptOptions{})
	require.NoError(t, err)

	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Equal(t, "needs setup: repos is required", res.Detail)
	assert.NotContains(t, res.Detail, "filter", "the filter was not the reason and was not evaluated")
	assert.Empty(t, starter.snapshot(), "nothing is started")
	events := repo.eventsFor(trig.ID)
	require.Len(t, events, 1)
	assert.Equal(t, "needs setup: repos is required", events[0].OutcomeDetail, "the history says it too")

	// Once set up, the same event is judged by the filter as before.
	trig.Params = map[string]any{"repos": []any{"acme/web"}}
	res, err = intake.Accept(context.Background(), trig, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: trig.ID + ":e2",
		OccurredAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
		Payload:    issueIn("acme/web", 7), Sender: octocat,
	}, AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, res.Outcome)
}

func TestScheduledFireOfAnActivationThatNeedsSetupIsRecordedAsSkipped(t *testing.T) {
	repo := newFakeRepo()
	name := "nightly"
	trig := scheduleActivation(t)
	trig.WorkflowTrigger = &name
	trig.Params = map[string]any{}
	repo.triggers[trig.ID] = trig
	launcher := &fakeLauncher{}
	workflows := fakeWorkflows{yaml: map[string]string{"triage": `name: triage
inputs:
  repos: {type: array}
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: sweep
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`}}

	out, err := NewFirer(repo, launcher).WithWorkflows(workflows).Fire(context.Background(), fireReq(trig.ID))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Equal(t, "needs setup: repos is required", out.Reason)
	assert.Empty(t, launcher.snapshot())
}
