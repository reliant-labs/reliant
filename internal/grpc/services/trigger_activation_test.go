// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The YAML of a workflow that declares one trigger of every kind.
const declaringWorkflowYAML = `name: triage
inputs:
  issue_number:
    type: integer
    default: 0
  mode:
    type: string
    default: ""
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *", timezone: America/New_York}
  - name: hook
    webhook: {}
    filter: "trigger.payload.body.ok == true"
  - name: new-issue
    integration: {integration: test, events: [issues.opened]}
    filter: "trigger.payload.data.issue.number > 0"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
  - name: after-deploy
    workflow_event: {outcomes: [failed]}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// saveWorkflow stores YAML as one of the user's complete workflows: the
// state a declaration must be in for anyone to activate it.
func (e *triggerTestEnv) saveWorkflow(t *testing.T, slug, yaml string) {
	t.Helper()
	now := time.Now().UTC()
	existing, err := e.repo.GetWorkflowDraftBySlug(context.Background(), e.userID, slug)
	require.NoError(t, err)
	if existing != nil {
		require.NoError(t, e.repo.UpdateWorkflowDraftDefinition(context.Background(), existing.ID, slug, slug, yaml, db.WorkflowDraftStatusComplete))
		return
	}
	require.NoError(t, e.repo.CreateWorkflowDraft(context.Background(), &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: e.userID, Name: slug, Slug: slug,
		Definition: yaml, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now,
	}))
}

func (e *triggerTestEnv) activation(name string, mutate func(*reliantv1.TriggerDefinition)) *reliantv1.TriggerDefinition {
	return e.definition(func(d *reliantv1.TriggerDefinition) {
		d.Name = "my " + name
		d.Workflow = "triage"
		d.Source = &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: name}
		if mutate != nil {
			mutate(d)
		}
	})
}

func TestActivateDeclaredSchedule(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)

	got := env.create(t, env.activation("nightly", nil))

	assert.Equal(t, "nightly", got.GetWorkflowTrigger())
	sched := got.GetSchedule()
	require.NotNil(t, sched, "an activation renders the declaration's source")
	assert.Equal(t, []string{"0 9 * * *"}, sched.GetCron())
	assert.Equal(t, "America/New_York", sched.GetTimezone())

	stored, err := env.repo.GetTrigger(context.Background(), got.GetId())
	require.NoError(t, err)
	assert.Equal(t, core.TriggerKindSchedule, stored.Kind)
	require.NotNil(t, stored.WorkflowTrigger)
	assert.Equal(t, "nightly", *stored.WorkflowTrigger)
	assert.Equal(t, 1, env.backend.syncCount(), "a declared schedule converges a Temporal schedule like any other")
}

func TestActivateDeclaredWebhookAndIntegration(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)

	hook, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.activation("hook", nil)}))
	require.NoError(t, err)
	assert.NotEmpty(t, hook.Msg.GetWebhook().GetToken(), "a declared webhook gets a token like any other")
	assert.Equal(t, "trigger.payload.body.ok == true", hook.Msg.GetTrigger().GetFilter(), "the filter is the declaration's")

	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)
	issue := env.create(t, env.activation("new-issue", func(d *reliantv1.TriggerDefinition) { d.ConnectionId = &conn.ID }))
	assert.Equal(t, conn.ID, issue.GetConnectionId())
	assert.Equal(t, "test", issue.GetIntegration().GetIntegration())
	assert.Equal(t, []string{"issues.opened"}, issue.GetIntegration().GetEvents())

	event := env.create(t, env.activation("after-deploy", nil))
	assert.Equal(t, []string{"failed"}, event.GetWorkflowEvent().GetOutcomes())
}

func TestActivateDeclaredTriggerRejects(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)

	cases := []struct {
		name   string
		def    *reliantv1.TriggerDefinition
		code   connect.Code
		reason string
	}{
		{
			name:   "unknown declaration",
			def:    env.activation("nope", nil),
			code:   connect.CodeNotFound,
			reason: `does not declare a trigger named "nope"`,
		},
		{
			name:   "workflow required",
			def:    env.activation("nightly", func(d *reliantv1.TriggerDefinition) { d.Workflow = "" }),
			code:   connect.CodeInvalidArgument,
			reason: "workflow",
		},
		{
			name:   "unknown workflow",
			def:    env.activation("nightly", func(d *reliantv1.TriggerDefinition) { d.Workflow = "no-such-workflow" }),
			code:   connect.CodeNotFound,
			reason: "no-such-workflow",
		},
		{
			name:   "filter belongs to the declaration",
			def:    env.activation("hook", func(d *reliantv1.TriggerDefinition) { d.Filter = "true" }),
			code:   connect.CodeInvalidArgument,
			reason: "filter",
		},
		{
			name: "param shadows a declared input",
			def: env.activation("hook", func(d *reliantv1.TriggerDefinition) {
				d.Workflow = "triage"
				d.Source = &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: "new-issue"}
				d.Params = map[string]*structpb.Value{"issue_number": structpb.NewNumberValue(3)}
			}),
			code:   connect.CodeInvalidArgument,
			reason: "issue_number",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: tc.def}))
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err), err.Error())
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

// An activation's params may set inputs the declaration does not map.
func TestActivationParamsForUnmappedInputs(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	got := env.create(t, env.activation("nightly", func(d *reliantv1.TriggerDefinition) {
		d.Params = map[string]*structpb.Value{"mode": structpb.NewStringValue("deep")}
	}))
	assert.Equal(t, "deep", got.GetParams()["mode"].GetStringValue())
}

// Editing the workflow changes what every activation does. Removing or
// renaming the declaration leaves the activation BROKEN — reported, not
// dropped, and not fired.
func TestActivationHealthReportsABrokenDeclaration(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	got := env.create(t, env.activation("nightly", nil))

	list := func() *reliantv1.Trigger {
		resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
		require.NoError(t, err)
		for _, tr := range resp.Msg.GetTriggers() {
			if tr.GetId() == got.GetId() {
				return tr
			}
		}
		t.Fatalf("trigger %s not listed", got.GetId())
		return nil
	}
	assert.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, list().GetHealth().GetStatus())

	// The declaration is renamed: the activation is broken, says why, and
	// is still listed.
	env.saveWorkflow(t, "triage", `name: triage
triggers:
  - name: morning
    schedule: {cron: "0 9 * * *"}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`)
	broken := list()
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, broken.GetHealth().GetStatus())
	assert.Contains(t, broken.GetHealth().GetLastFailureDetail(), `no longer declares a trigger named "nightly"`)
	assert.Contains(t, broken.GetHealth().GetLastFailureDetail(), "morning")

	// GetTrigger agrees with ListTriggers.
	one, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: got.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, one.Msg.GetTrigger().GetHealth().GetStatus())

	// Restoring it heals it.
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	assert.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, list().GetHealth().GetStatus())
}

// An activation's kind is its identity: it cannot become another kind, nor
// an ad hoc trigger, by update.
func TestUpdateActivationKeepsItsDeclaration(t *testing.T) {
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	got := env.create(t, env.activation("nightly", nil))

	disabled := false
	resp, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id:      got.GetId(),
		Trigger: env.activation("nightly", func(d *reliantv1.TriggerDefinition) { d.Enabled = &disabled; d.Message = "changed" }),
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetTrigger().GetEnabled())
	assert.Equal(t, "changed", resp.Msg.GetTrigger().GetMessage())
	assert.Equal(t, "nightly", resp.Msg.GetTrigger().GetWorkflowTrigger())

	// Inline source on an activation: rejected.
	_, err = env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: got.GetId(), Trigger: env.definition(nil),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Another declaration of another kind: rejected (the kind is not updatable).
	_, err = env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: got.GetId(), Trigger: env.activation("after-deploy", nil),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
