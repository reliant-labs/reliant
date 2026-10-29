// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func TestActivateResolvesTheAppsChoices(t *testing.T) {
	f := &activateFlags{
		workflow:     "pr-review",
		projectID:    "proj-1",
		machine:      "cloud",
		daemonLister: fakeDaemonLister{{DaemonId: "daemon-2", Hostname: "cloud"}},
		paramsJSON:   `{"repos":["acme/api"],"promote_prod":true}`,
		params:       []string{"promote_prod=false", "review_model={\"id\":\"m\"}"},
		connections:  []string{"github=conn-1"},
		actingAs:     "owner",
		namePrefix:   "PR pipeline · pr-review",
	}
	c, err := f.resolve(context.Background(), newTriggerActivateCmd(), nil)
	require.NoError(t, err)
	req := f.enableRequest(c)
	assert.Equal(t, "pr-review", req.GetWorkflow())
	assert.Equal(t, "proj-1", req.GetProjectId())
	assert.Equal(t, "daemon-2", req.GetDaemonId(), "a hostname resolves to its daemon id")
	assert.Equal(t, []any{"acme/api"}, req.GetParams()["repos"].AsInterface())
	assert.Equal(t, false, req.GetParams()["promote_prod"].AsInterface(), "--param wins over --params-json")
	assert.Equal(t, map[string]any{"id": "m"}, req.GetParams()["review_model"].AsInterface())
	assert.Equal(t, map[string]string{"github": "conn-1"}, req.GetConnectionIds())
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, req.GetActingIdentity())
	assert.Equal(t, "PR pipeline · pr-review", req.GetNamePrefix())

	def := f.triggerDefinition("queued", "github", c)
	assert.Equal(t, "PR pipeline · pr-review · queued", def.GetName())
	assert.Equal(t, "queued", def.GetWorkflowTrigger())
	assert.Equal(t, "conn-1", def.GetConnectionId())
}

type fakeWorkflowGetter struct{ wf *reliantv1.Workflow }

func (g fakeWorkflowGetter) GetWorkflow(
	_ context.Context,
	_ *connect.Request[reliantv1.GetWorkflowRequest],
) (*connect.Response[reliantv1.GetWorkflowResponse], error) {
	return connect.NewResponse(&reliantv1.GetWorkflowResponse{Workflow: g.wf}), nil
}

func integrationTrigger(name, integration string) *reliantv1.WorkflowTrigger {
	return &reliantv1.WorkflowTrigger{Name: name, Source: &reliantv1.WorkflowTrigger_Integration{
		Integration: &reliantv1.IntegrationSource{Integration: integration}}}
}

// A per-trigger activation routes each --connection to the triggers of ITS
// integration, as EnableWorkflow does, and never drops one silently.
func TestActivatePerTriggerRoutesConnectionsByIntegration(t *testing.T) {
	wf := &reliantv1.Workflow{Triggers: []*reliantv1.WorkflowTrigger{
		integrationTrigger("pr", "github"), integrationTrigger("msg", "slack"), integrationTrigger("cron", ""),
	}}
	f := &activateFlags{workflow: "w", triggers: []string{"pr", "msg"}, workflowGetter: fakeWorkflowGetter{wf}}
	c := &activateChoices{projectID: "p", connections: map[string]string{"github": "gh-1", "slack": "sl-1"}}
	integrations, err := f.declaredIntegrations(context.Background(), nil, c)
	require.NoError(t, err)
	assert.Equal(t, "gh-1", f.triggerDefinition("pr", integrations["pr"], c).GetConnectionId())
	assert.Equal(t, "sl-1", f.triggerDefinition("msg", integrations["msg"], c).GetConnectionId())

	f.triggers = []string{"pr"}
	_, err = f.declaredIntegrations(context.Background(), nil, c)
	assert.ErrorContains(t, err, "--connection slack=… matches none of the named triggers")

	f.triggers = []string{"nope"}
	_, err = f.declaredIntegrations(context.Background(), nil, c)
	assert.ErrorContains(t, err, `declares no trigger "nope"`)

	f.triggers = []string{"cron"}
	none, err := f.declaredIntegrations(context.Background(), nil, &activateChoices{projectID: "p"})
	require.NoError(t, err)
	assert.Nil(t, none, "no --connection, nothing to route or fetch")
	assert.Nil(t, f.triggerDefinition("cron", "", c).ConnectionId)
}

func TestActivateNeedsExactlyOnePlaceToRun(t *testing.T) {
	_, err := (&activateFlags{workflow: "w", projectID: "p"}).resolve(context.Background(), newTriggerActivateCmd(), nil)
	assert.ErrorContains(t, err, "exactly one of --machine or --no-machine")
	_, err = (&activateFlags{workflow: "w", projectID: "p", machine: "m", noMachine: true}).resolve(context.Background(), newTriggerActivateCmd(), nil)
	assert.ErrorContains(t, err, "exactly one of --machine or --no-machine")
	c, err := (&activateFlags{workflow: "w", projectID: "p", noMachine: true}).resolve(context.Background(), newTriggerActivateCmd(), nil)
	require.NoError(t, err)
	assert.True(t, c.noMachine)
}

func TestActivateRejectsMalformedFlags(t *testing.T) {
	_, err := mergeActivateParams(`[1,2]`, nil)
	assert.ErrorContains(t, err, "--params-json must be a JSON object")
	_, err = parseActivateConnections([]string{"github"})
	assert.ErrorContains(t, err, "integration=connection-id")
	_, err = parseActingIdentity("bot")
	assert.ErrorContains(t, err, "owner or app")
}

func TestWorkflowOutcomeAndHealthLabels(t *testing.T) {
	assert.Equal(t, "already enabled", workflowOutcomeLabel(reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED))
	assert.Equal(t, "needs setup", triggerHealthLabel(&reliantv1.Trigger{Health: &reliantv1.TriggerHealth{Status: reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_NEEDS_SETUP}}))
	assert.Equal(t, "-", triggerHealthLabel(&reliantv1.Trigger{}))
}
