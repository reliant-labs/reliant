// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// reactorYAML declares a workflow_event trigger on code-review failures and
// maps the failed run's chat id into an input.
const reactorYAML = `name: reactor
inputs:
  failed_chat:
    type: string
    default: ""
triggers:
  - name: on-review-failure
    workflow_event: {workflows: [code-review], outcomes: [failed]}
    filter: "trigger.payload.workflow_name == 'code-review'"
    inputs:
      failed_chat: "{{ trigger.payload.chat_id }}"
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`

func (f *fixture) saveDeclaringWorkflow(slug, yaml string) {
	f.t.Helper()
	ctx := context.Background()
	if existing, err := f.repo.GetWorkflowDraftBySlug(ctx, f.userID, slug); err == nil && existing != nil {
		require.NoError(f.t, f.repo.UpdateWorkflowDraftDefinition(ctx, existing.ID, slug, slug, yaml, db.WorkflowDraftStatusComplete))
		return
	}
	now := time.Now().UTC()
	require.NoError(f.t, f.repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: f.userID, Name: slug, Slug: slug, Definition: yaml,
		Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))
}

// addActivation stores an activation of a declared workflow_event trigger,
// with the projection activation would have written.
func (f *fixture) addActivation(workflow, declared string, src Source) *core.Trigger {
	f.t.Helper()
	config, err := json.Marshal(src)
	require.NoError(f.t, err)
	now := time.Now().UTC()
	tr := &core.Trigger{
		ID: uuid.NewString(), UserID: f.userID, ProjectID: f.projectID, Name: "my " + declared,
		Kind: core.TriggerKindWorkflowEvent, Enabled: true, Workflow: workflow, WorkflowTrigger: &declared,
		Message: "react to the failure", DaemonID: f.daemonID, Config: config,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(f.t, f.repo.CreateTrigger(context.Background(), tr))
	return tr
}

func TestDispatch_ActivationFiresWithItsDeclaredInputs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.dispatcher.WithWorkflows(triggers.LaunchWorkflows{Repo: f.repo})
	f.saveDeclaringWorkflow("reactor", reactorYAML)
	tr := f.addActivation("reactor", "on-review-failure", Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}})

	source := f.humanRun("code-review")
	results, err := f.dispatcher.Dispatch(ctx, f.finish(source, core.RunEventFailed))
	require.NoError(t, err)
	launched := launchedChat(t, results, tr.ID)

	launchEv, err := f.repo.GetTriggerEventByChatID(ctx, launched)
	require.NoError(t, err)
	start, _ := launchEv.Payload["start"].(map[string]any)
	params, _ := start["params"].(map[string]any)
	assert.Equal(t, source, params["failed_chat"], "the declared input carries the failed run's chat id")
}

// The declaration's source is what matches, read now: an edit narrowing the
// outcomes takes effect on the next event without touching the row.
func TestDispatch_ActivationMatchesTheDeclarationNotItsProjection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.dispatcher.WithWorkflows(triggers.LaunchWorkflows{Repo: f.repo})
	f.saveDeclaringWorkflow("reactor", reactorYAML)
	// A stale projection that would match "finished" too.
	tr := f.addActivation("reactor", "on-review-failure", Source{Workflows: []string{"code-review"}})

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFinished))
	require.NoError(t, err)
	for _, r := range results {
		assert.NotEqual(t, tr.ID, r.TriggerID, "the declaration listens to failures only")
	}
	assert.Equal(t, 0, f.starter.count())
}

// A declaration removed from the workflow: the activation records why on the
// events it used to match, and launches nothing.
func TestDispatch_BrokenActivationIsRecordedNotFired(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.dispatcher.WithWorkflows(triggers.LaunchWorkflows{Repo: f.repo})
	f.saveDeclaringWorkflow("reactor", reactorYAML)
	tr := f.addActivation("reactor", "on-review-failure", Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}})
	f.saveDeclaringWorkflow("reactor", "name: reactor\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hi\"\n")

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFailed))
	require.NoError(t, err)
	assert.Equal(t, 0, f.starter.count())
	var found bool
	for _, r := range results {
		if r.TriggerID == tr.ID {
			found = true
			assert.Equal(t, core.TriggerEventFailed, r.Outcome)
			assert.Contains(t, r.Detail, `no longer declares a trigger named "on-review-failure"`)
		}
	}
	assert.True(t, found, "the broken activation's verdict is recorded")
}
