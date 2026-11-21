// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

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

// checkedInputWorkflow declares a check and an input the trigger maps from the
// event's PR number, which the test's event does not carry.
const checkedInputWorkflow = `name: ci
check: {name: reliant/ci}
inputs:
  pr_number:
    type: integer
triggers:
  - name: pr
    integration:
      integration: github
      events: [pull_request.synchronize]
    inputs:
      pr_number: "{{ trigger.payload.data.pull_request.number }}"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// A checked workflow whose firing is refused before launch (its daemon was
// deleted, or the event does not fit a declared input) still opens its check,
// and the syncer concludes that check failure from the refused firing: a
// required check never stays missing on GitHub because the run could not
// start.
func TestEventFireRefusedBeforeLaunchConcludesItsCheckFailed(t *testing.T) {
	const headSHA = "6dcb09b5b57875f334f61aebed695e2e4193db5e"
	for _, tc := range []struct {
		name, yaml, daemon, detail string
	}{
		{name: "missing daemon", yaml: checkedWorkflow, daemon: "daemon-deleted", detail: "trigger's daemon no longer exists"},
		{name: "invalid declared input", yaml: checkedInputWorkflow, daemon: testDaemonID, detail: "inputs could not be read from this event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newFakeRepo()
			name := "pr"
			trig := &core.Trigger{
				ID: "t-ci", UserID: "user-1", ProjectID: "project-1", Name: "ci",
				Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "ci", WorkflowTrigger: &name,
				DaemonID: tc.daemon, ActingIdentity: core.ActingIdentityApp,
				Config: json.RawMessage(`{"integration":"github","events":["pull_request.synchronize"]}`),
			}
			repo.triggers[trig.ID] = trig
			workflows := fakeWorkflows{yaml: map[string]string{"ci": tc.yaml}}
			res, err := NewIntake(repo, &recordingStarter{}, "q").WithWorkflows(workflows).Accept(ctx, trig, InboundEvent{
				Kind: core.TriggerEventKindIntegration, DedupeKey: "t-ci:d1", Sender: octocat,
				Payload: map[string]any{"attributes": map[string]any{"repository": "reliant-labs/reliant"},
					"data": map[string]any{"pull_request": map[string]any{"head": map[string]any{"sha": headSHA}}}},
			}, AcceptOptions{})
			require.NoError(t, err)
			require.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)

			// The real check service over an in-memory row, writing to a
			// recorded GitHub.
			var rows []*core.WorkflowCheckRun
			store := &workflowcheck.MockStore{
				CreateWorkflowCheckRunFunc: func(_ context.Context, row *core.WorkflowCheckRun) (bool, error) {
					rows = append(rows, row)
					return true, nil
				},
				ClaimWorkflowCheckRunsFunc: func(context.Context, string, time.Time, time.Time, int) ([]*core.WorkflowCheckRun, error) {
					return rows, nil
				},
				RenewWorkflowCheckRunLeasesFunc: func(_ context.Context, _ string, leases []core.WorkflowCheckLease, _ time.Time) ([]string, error) {
					ids := make([]string, 0, len(leases))
					for _, l := range leases {
						ids = append(ids, l.ID)
					}
					return ids, nil
				},
				ObserveWorkflowCheckRunFunc: func(_ context.Context, eventID string) (*core.WorkflowCheckObservation, error) {
					for _, ev := range repo.eventsFor(trig.ID) {
						if ev.ID == eventID {
							return &core.WorkflowCheckObservation{EventOutcome: ev.Outcome, EventDetail: ev.OutcomeDetail}, nil
						}
					}
					return nil, core.ErrWorkflowCheckRunNotFound
				},
				SaveWorkflowCheckRunSyncFunc: func(context.Context, *core.WorkflowCheckRun) error { return nil },
			}
			runs := &workflowcheck.MockCheckRuns{
				FindFunc:   func(context.Context, string, string, string) (int64, error) { return 0, nil },
				CreateFunc: func(context.Context, workflowcheck.Write) (int64, error) { return 7, nil },
			}
			checks := workflowcheck.New(workflowcheck.Deps{Store: store, GitHub: &workflowcheck.MockGitHub{
				CheckRunsFunc: func(context.Context, workflowcheck.Identity, string, string) (workflowcheck.CheckRuns, error) {
					return runs, nil
				},
			}})

			launcher := &fakeLauncher{}
			_, err = NewEventFirer(repo, launcher).WithWorkflows(workflows).WithChecks(checks).Fire(ctx, EventFireInput{
				TriggerID: trig.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: "t-ci:d1",
			})
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, err, &appErr, "the refusal ends the fire")
			require.True(t, appErr.NonRetryable())
			require.Empty(t, launcher.snapshot(), "a refused firing launches nothing")
			events := repo.eventsFor(trig.ID)
			require.Len(t, events, 1)
			require.Equal(t, core.TriggerEventFailed, events[0].Outcome)
			require.Contains(t, events[0].OutcomeDetail, tc.detail)

			require.Len(t, rows, 1, "the refused firing opened its check")
			assert.Equal(t, events[0].ID, rows[0].TriggerEventID)
			assert.Equal(t, "reliant/ci", rows[0].Name)
			assert.Equal(t, headSHA, rows[0].HeadSHA)

			synced, err := checks.SyncOnce(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, synced)
			creates := runs.Calls("Create")
			require.Len(t, creates, 1)
			w, _ := creates[0].Args[1].(workflowcheck.Write)
			assert.Equal(t, core.CheckStatusCompleted, w.Status)
			assert.Equal(t, workflowcheck.ConclusionFailure, w.Conclusion, "the check says the run could not start")
			assert.NotNil(t, rows[0].FinishedAt, "a concluded check is finished")
		})
	}
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
