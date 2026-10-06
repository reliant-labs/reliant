// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/triggers"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
)

// activatedWorkflowYAML declares the triggers the end-to-end tests activate.
// Its run node does nothing; what is under test is what the launch carried.
const activatedWorkflowYAML = `name: triage
inputs:
  model:
    type: any
    default: {}
  issue_number:
    type: integer
    default: 0
  report_date:
    type: string
    default: ""
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    inputs:
      report_date: "{{ trigger.scheduled_for }}"
  - name: new-issue
    integration:
      integration: test
      events: [issues.opened]
      match: {repository: acme/app}
    filter: "trigger.payload.data.number > 5"
    inputs:
      issue_number: "{{ trigger.payload.data.number }}"
entry: [noop]
nodes:
  - id: noop
    type: run
    command: "true"
`

// Activation, end to end on a real Temporal dev server: a declared trigger
// is activated through the real TriggerService from a workflow's YAML, fires
// through the real fire workflows and the real launcher, and the run it
// starts carries the declaration's inputs, evaluated against the event.
func TestDeclaredTriggerActivationsFireEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-activation-e2e-" + uuid.NewString()

	declarations := triggers.LaunchWorkflows{Repo: repo}
	launcher := launch.NewLauncher(repo, threads.NewService(repo), temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)), taskQueue, nil)
	w := worker.New(temporalClient, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(triggers.TriggerFireWorkflow, workflow.RegisterOptions{Name: triggers.FireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewFirer(repo, launcher).WithWorkflows(declarations).Fire,
		activity.RegisterOptions{Name: triggers.FireActivityName})
	w.RegisterWorkflowWithOptions(triggers.TriggerEventFireWorkflow, workflow.RegisterOptions{Name: triggers.EventFireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewEventFirer(repo, launcher).WithWorkflows(declarations).Fire,
		activity.RegisterOptions{Name: triggers.EventFireActivityName})
	w.RegisterWorkflowWithOptions(func(workflow.Context, any) error { return nil }, workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic})
	require.NoError(t, w.Start())
	defer w.Stop()

	provider := webhook.NewTestProvider("e2e-secret")
	registry := webhook.NewRegistry()
	require.NoError(t, registry.Register(provider))
	intake := triggers.NewIntake(repo, temporalClient, taskQueue).WithWorkflows(declarations)
	const publicURL = "https://hooks.example.com"
	inbound := webhook.NewInbound(repo, intake, registry, nil, publicURL)
	mux := http.NewServeMux()
	inbound.Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	server := httptest.NewServer(mux)
	defer server.Close()

	backend := triggers.NewBackend(temporalClient.ScheduleClient(), temporalClient, repo, taskQueue)
	svc := NewTriggerService(repo, backend, backend).WithInbound(InboundOptions{
		PublicURL: publicURL, Catalog: registry, Intake: intake,
	})

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Activation E2E", Path: t.TempDir(),
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	createMainWorktree(t, repo, projectID, now)
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: userID, Name: "triage", Slug: "triage", Definition: activatedWorkflowYAML,
		Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now,
	}))
	authCtx := context.WithValue(ctx, auth.UserIDContextKey, userID)

	activate := func(name, declared string, mutate func(*reliantv1.TriggerDefinition)) *reliantv1.Trigger {
		def := &reliantv1.TriggerDefinition{
			Name: name, ProjectId: projectID, DaemonId: daemonID, Workflow: "triage",
			Message: "Handle it.", Params: mockModelTriggerParams(t),
			Source: &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: declared},
		}
		if mutate != nil {
			mutate(def)
		}
		resp, err := svc.CreateTrigger(authCtx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: def}))
		require.NoError(t, err)
		return resp.Msg.GetTrigger()
	}
	launchedParams := func(ev *core.TriggerEvent) map[string]any {
		start, _ := ev.Payload["start"].(map[string]any)
		params, _ := start["params"].(map[string]any)
		return params
	}

	// --- schedule ---------------------------------------------------------
	sched := activate("my nightly", "nightly", nil)
	assert.Equal(t, []string{"0 9 * * *"}, sched.GetSchedule().GetCron())
	require.NotNil(t, sched.GetNextFireAt(), "a declared schedule converges a real Temporal schedule")
	_, err := svc.FireTrigger(authCtx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: sched.GetId()}))
	require.NoError(t, err)
	firedSched := waitForLaunched(t, repo, userID, sched.GetId())
	params := launchedParams(firedSched)
	scheduledFor, _ := firedSched.Payload["scheduled_for"].(string)
	require.NotEmpty(t, scheduledFor)
	assert.Equal(t, scheduledFor, params["report_date"], "the declared input reads the fire's slot")

	// --- integration ------------------------------------------------------
	conn := &core.Connection{
		ID: uuid.NewString(), OwnerKind: core.ConnectionOwnerUser, UserID: userID, IntegrationID: "test",
		AuthKind: "none", Name: "test", ExternalAccountID: strPtr("acct-1"), Status: core.ConnectionStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.Connections().CreateConnection(ctx, conn, nil,
		core.ConnectionEvent{ConnectionID: conn.ID, UserID: userID, Kind: core.ConnectionEventCreated, Actor: "user:" + userID}))
	issue := activate("my new issue", "new-issue", func(d *reliantv1.TriggerDefinition) { d.ConnectionId = &conn.ID })
	assert.Equal(t, conn.ID, issue.GetConnectionId())

	deliver := func(id string, number float64) {
		raw, err := json.Marshal(webhook.TestDelivery{ID: id, Account: "acct-1", Type: "issues.opened",
			Attributes: map[string]string{"repository": "acme/app"}, Data: map[string]any{"number": number}})
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/integrations/test/events", strings.NewReader(string(raw)))
		require.NoError(t, err)
		req.Header.Set(webhook.TestSignatureHeader, webhook.SignTestDelivery("e2e-secret", publicURL+"/integrations/test/events", raw))
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	deliver("d-small", 3) // the declaration's filter (number > 5) skips it
	deliver("d-big", 42)
	firedIssue := waitForLaunched(t, repo, userID, issue.GetId())
	assert.Equal(t, issue.GetId()+":d-big", firedIssue.DedupeKey)
	assert.Equal(t, float64(42), launchedParams(firedIssue)["issue_number"], "the event's number is the run's input")
	assertFirings(t, repo, userID, issue.GetId(), map[core.TriggerEventOutcome]int{
		core.TriggerEventLaunched: 1, core.TriggerEventSkipped: 1,
	})

	// --- the workflow is edited: the declaration is renamed ---------------
	// Every activation of it is now broken: reported in ListTriggers, and an
	// event that would have fired it is recorded as failed, not launched.
	draft, err := repo.GetWorkflowDraftBySlug(ctx, userID, "triage")
	require.NoError(t, err)
	renamed := strings.Replace(activatedWorkflowYAML, "- name: new-issue", "- name: issue-opened", 1)
	require.NoError(t, repo.UpdateWorkflowDraftDefinition(ctx, draft.ID, "triage", "triage", renamed, db.WorkflowDraftStatusComplete))

	list, err := svc.ListTriggers(authCtx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	for _, tr := range list.Msg.GetTriggers() {
		if tr.GetId() == issue.GetId() {
			assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, tr.GetHealth().GetStatus())
			assert.Contains(t, tr.GetHealth().GetLastFailureDetail(), "issue-opened")
		}
		if tr.GetId() == sched.GetId() {
			assert.NotEqual(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN, tr.GetHealth().GetStatus(),
				"the other declaration is untouched")
		}
	}
	deliver("d-after-rename", 99)
	assertFirings(t, repo, userID, issue.GetId(), map[core.TriggerEventOutcome]int{
		core.TriggerEventLaunched: 1, core.TriggerEventSkipped: 1, core.TriggerEventFailed: 1,
	})
}
