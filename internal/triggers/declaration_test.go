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
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// fakeWorkflows resolves workflow refs from YAML held in memory.
type fakeWorkflows struct {
	yaml map[string]string
	err  error
}

func (f fakeWorkflows) ResolveRunWorkflow(_ context.Context, _, ref, _ string) (*reliantv1.Workflow, error) {
	if f.err != nil {
		return nil, f.err
	}
	y, ok := f.yaml[ref]
	if !ok {
		return nil, errors.New("workflow '" + ref + "' not found")
	}
	return wfyaml.ParseWorkflow([]byte(y))
}

const declaringWorkflow = `name: triage
inputs:
  issue_number:
    type: integer
    default: 0
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.data.issue.number > 0"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
  - name: nightly
    schedule: {cron: "0 9 * * *"}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func activation(name string, kind core.TriggerKind) *core.Trigger {
	return &core.Trigger{ID: "t1", UserID: "u1", ProjectID: "p1", Workflow: "triage", Kind: kind, WorkflowTrigger: &name}
}

func TestResolveDeclaration(t *testing.T) {
	workflows := fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}

	decl, err := ResolveDeclaration(context.Background(), workflows, activation("new-issue", core.TriggerKindIntegration))
	require.NoError(t, err)
	assert.Equal(t, core.TriggerKindIntegration, decl.Source.Kind)
	assert.Equal(t, "github", decl.Source.Integration)
	assert.Equal(t, "trigger.payload.data.issue.number > 0", decl.Filter)
	assert.Equal(t, map[string]string{"issue_number": "{{ trigger.payload.data.issue.number }}"}, decl.Inputs)
	var cfg core.IntegrationConfig
	require.NoError(t, json.Unmarshal(decl.Source.Config, &cfg))
	assert.Equal(t, []string{"issues.opened"}, cfg.Events)

	// An ad hoc trigger has no declaration.
	_, err = ResolveDeclaration(context.Background(), workflows, &core.Trigger{ID: "t2", Workflow: "triage", Kind: core.TriggerKindWebhook})
	assert.ErrorIs(t, err, ErrNotAnActivation)
}

// Every way an activation can stop matching its workflow is a
// *DeclarationError naming the reason, never a silent fallback.
func TestResolveDeclarationBroken(t *testing.T) {
	renamed := `name: triage
triggers:
  - name: issue-opened
    integration: {integration: github, events: [issues.opened]}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	kindChanged := `name: triage
triggers:
  - name: new-issue
    webhook: {}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	invalid := `name: triage
triggers:
  - name: new-issue
    integration: {integration: github}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	cases := []struct {
		name     string
		yaml     string
		missing  bool
		kind     core.TriggerKind
		reason   string
		original string
	}{
		{name: "workflow gone", missing: true, kind: core.TriggerKindIntegration, reason: "workflow"},
		{name: "declaration renamed", yaml: renamed, kind: core.TriggerKindIntegration, reason: `no longer declares a trigger named "new-issue"`},
		{name: "kind changed", yaml: kindChanged, kind: core.TriggerKindIntegration, reason: "now a webhook trigger"},
		{name: "declaration invalid", yaml: invalid, kind: core.TriggerKindIntegration, reason: "events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workflows := fakeWorkflows{yaml: map[string]string{}}
			if !tc.missing {
				workflows.yaml["triage"] = tc.yaml
			}
			_, err := ResolveDeclaration(context.Background(), workflows, activation("new-issue", tc.kind))
			var declErr *DeclarationError
			require.ErrorAs(t, err, &declErr)
			assert.Contains(t, declErr.Error(), tc.reason)
			assert.Contains(t, declErr.Error(), "new-issue")
		})
	}
}

// A store failure while reading the workflow is NOT a broken declaration:
// it is retryable, and must not be recorded as the trigger being broken.
func TestResolveDeclarationStoreFailureIsNotBroken(t *testing.T) {
	_, err := ResolveDeclaration(context.Background(), fakeWorkflows{err: &lookupFailure{}}, activation("new-issue", core.TriggerKindIntegration))
	require.Error(t, err)
	var declErr *DeclarationError
	assert.False(t, errors.As(err, &declErr))
}

type lookupFailure struct{}

func (*lookupFailure) Error() string   { return "db down" }
func (*lookupFailure) Retryable() bool { return true }

// The connection an integration activation listens through belongs to one
// integration. A declaration edited to another integration cannot be
// followed: the activation is broken until it is re-activated.
func TestResolveDeclarationIntegrationChanged(t *testing.T) {
	slack := `name: triage
triggers:
  - name: new-issue
    integration: {integration: slack, events: [message]}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	trig := activation("new-issue", core.TriggerKindIntegration)
	trig.Config = json.RawMessage(`{"integration":"github","events":["issues.opened"]}`)
	_, err := ResolveDeclaration(context.Background(), fakeWorkflows{yaml: map[string]string{"triage": slack}}, trig)
	var declErr *DeclarationError
	require.ErrorAs(t, err, &declErr)
	assert.Contains(t, declErr.Error(), "slack")
	assert.Contains(t, declErr.Error(), "github")
}

func TestDeclaredInputsMergeOverParams(t *testing.T) {
	root := map[string]any{"payload": map[string]any{"data": map[string]any{"issue": map[string]any{"number": int64(7)}}}}
	params, err := MergeDeclaredInputs(
		map[string]any{"issue_number": 1, "mode": "triage"},
		map[string]string{"issue_number": "{{ trigger.payload.data.issue.number }}"},
		root,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(7), params["issue_number"], "the event's value wins over a stored param")
	assert.Equal(t, "triage", params["mode"], "params the declaration does not map pass through")

	_, err = MergeDeclaredInputs(nil, map[string]string{"issue_number": "{{ trigger.payload.data.pr.number }}"}, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issue_number")
}
