// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
)

type oneWorkflow struct {
	wf  *reliantv1.Workflow
	err error
}

func (o oneWorkflow) ResolveRunWorkflow(context.Context, string, string, string) (*reliantv1.Workflow, error) {
	return o.wf, o.err
}

type recordingOpener struct{ firings []workflowcheck.Firing }

func (r *recordingOpener) Open(_ context.Context, f workflowcheck.Firing) error {
	r.firings = append(r.firings, f)
	return nil
}

const checkedWorkflow = `name: ci
check: {name: reliant/ci}
triggers:
  - name: pr
    integration:
      integration: github
      events: [pull_request.synchronize]
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// An event fire of a workflow with check: opens the check before it launches
// the run, against the event it recorded; a redriven fire opens it again,
// which the opener answers idempotently.
func TestEventFireOpensTheWorkflowsCheckBeforeLaunching(t *testing.T) {
	repo := newFakeRepo()
	name := "pr"
	trig := &core.Trigger{
		ID: "t-ci", UserID: "user-1", ProjectID: "project-1", Name: "ci",
		Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "ci", WorkflowTrigger: &name,
		DaemonID: testDaemonID, ActingIdentity: core.ActingIdentityApp,
		Config: json.RawMessage(`{"integration":"github","events":["pull_request.synchronize"]}`),
	}
	repo.triggers[trig.ID] = trig
	workflows := fakeWorkflows{yaml: map[string]string{"ci": checkedWorkflow}}
	res, err := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(workflows).Accept(context.Background(), trig, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: "t-ci:d1", Sender: octocat,
		Payload: map[string]any{"attributes": map[string]any{"repository": "reliant-labs/reliant"},
			"data": map[string]any{"pull_request": map[string]any{"head": map[string]any{"sha": "6dcb09b5b57875f334f61aebed695e2e4193db5e"}}}},
	}, AcceptOptions{})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)

	opener := &recordingOpener{}
	launcher := &fakeLauncher{}
	out, err := NewEventFirer(repo, launcher).WithWorkflows(workflows).WithChecks(opener).Fire(context.Background(), EventFireInput{
		TriggerID: trig.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: "t-ci:d1",
	})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	require.Len(t, opener.firings, 1)
	f := opener.firings[0]
	assert.Equal(t, "reliant/ci", f.Workflow.GetCheck().GetName())
	assert.Equal(t, "t-ci:d1", f.Event.DedupeKey, "the check belongs to the recorded firing")
	assert.NotEmpty(t, f.Event.ID)
	assert.Equal(t, trig.ID, f.Trigger.ID)
}

func TestOpenCheckOpensOnlyAWorkflowsCheck(t *testing.T) {
	ctx := context.Background()
	trigger := &core.Trigger{ID: "t1", UserID: "u1", Name: "ci", Workflow: "reliant-ci"}
	ev := &core.TriggerEvent{ID: "e1", Kind: core.TriggerEventKindIntegration,
		Payload: map[string]any{"attributes": map[string]any{"repository": "reliant-labs/reliant"}}}
	checked := &reliantv1.Workflow{Name: "reliant-ci", Check: &reliantv1.WorkflowCheck{Name: "reliant/ci"}}

	opener := &recordingOpener{}
	require.NoError(t, openCheck(ctx, opener, oneWorkflow{wf: checked}, nil, trigger, nil, ev))
	require.Len(t, opener.firings, 1)
	f := opener.firings[0]
	require.Same(t, checked, f.Workflow)
	require.Same(t, ev, f.Event)
	require.Same(t, trigger, f.Trigger)
	// The scope is the `trigger` root the run sees, for check.head_sha.
	payload, _ := f.Scope.Trigger["payload"].(map[string]any)
	require.Equal(t, ev.Payload["attributes"], payload["attributes"])

	opener = &recordingOpener{}
	require.NoError(t, openCheck(ctx, opener, oneWorkflow{wf: &reliantv1.Workflow{Name: "plain"}}, nil, trigger, nil, ev))
	require.Empty(t, opener.firings, "a workflow without check: opens nothing")

	require.NoError(t, openCheck(ctx, nil, oneWorkflow{wf: checked}, nil, trigger, nil, ev), "a firer without checks opens nothing")

	err := openCheck(ctx, opener, oneWorkflow{err: errors.New("catalog unavailable")}, nil, trigger, nil, ev)
	require.Error(t, err, "a workflow that cannot be read retries the fire")
}
