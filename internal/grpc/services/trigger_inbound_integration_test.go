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

// The inbound path end to end, with nothing faked but the agent: a webhook
// trigger and an integration trigger created through the real TriggerService,
// deliveries POSTed to the real receivers over HTTP, recorded by the real
// intake, launched by the real event-fire workflow and launcher on a real
// Temporal dev server. Redeliveries must not launch twice, and a delivery
// for another user's account must never reach this user's trigger.
func TestInboundTriggersFireEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-inbound-e2e-" + uuid.NewString()

	launcher := launch.NewLauncher(repo, threads.NewService(repo), temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)), taskQueue, nil)
	w := worker.New(temporalClient, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(triggers.TriggerEventFireWorkflow, workflow.RegisterOptions{Name: triggers.EventFireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewEventFirer(repo, launcher).Fire, activity.RegisterOptions{Name: triggers.EventFireActivityName})
	w.RegisterWorkflowWithOptions(func(workflow.Context, any) error { return nil }, workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic})
	require.NoError(t, w.Start())
	defer w.Stop()

	provider := webhook.NewTestProvider("e2e-secret")
	registry := webhook.NewRegistry()
	require.NoError(t, registry.Register(provider))
	intake := triggers.NewIntake(repo, temporalClient, taskQueue)
	const publicURL = "https://hooks.example.com"
	inbound := webhook.NewInbound(repo, intake, registry, nil, publicURL)
	mux := http.NewServeMux()
	inbound.Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	server := httptest.NewServer(mux)
	defer server.Close()

	svc := NewTriggerService(repo, nil, nil).WithInbound(InboundOptions{
		PublicURL: publicURL, Catalog: registry, Intake: intake,
	})

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Inbound E2E", Path: t.TempDir(),
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	createMainWorktree(t, repo, projectID, now)
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	authCtx := context.WithValue(ctx, auth.UserIDContextKey, userID)

	base := func(name string) *reliantv1.TriggerDefinition {
		return &reliantv1.TriggerDefinition{
			Name: name, ProjectId: projectID, DaemonId: daemonID, Workflow: "builtin://agent",
			Message: "Handle the event.", Params: mockModelTriggerParams(t),
		}
	}

	// --- generic webhook -------------------------------------------------
	hookDef := base("deploy hook")
	hookDef.Filter = "trigger.payload.body.action == 'deploy'"
	hookDef.Source = &reliantv1.TriggerDefinition_Webhook{Webhook: &reliantv1.WebhookSource{}}
	created, err := svc.CreateTrigger(authCtx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: hookDef}))
	require.NoError(t, err)
	hook := created.Msg.GetTrigger()
	token := created.Msg.GetWebhook().GetToken()

	post := func(path, body string, headers map[string]string) int {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusNotFound, post("/hooks/"+hook.GetId()+"/whk_wrong", `{"action":"deploy"}`, nil))
	assert.Equal(t, http.StatusAccepted, post("/hooks/"+hook.GetId(), `{"action":"ping"}`,
		map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "ping-1"}), "a filter miss is still accepted")
	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusAccepted, post("/hooks/"+hook.GetId()+"/"+token, `{"action":"deploy","ref":"v1.2"}`,
			map[string]string{"Idempotency-Key": "deploy-1"}))
	}

	launchedHook := waitForLaunched(t, repo, userID, hook.GetId())
	chat, err := repo.GetChat(ctx, *launchedHook.ChatID)
	require.NoError(t, err)
	assert.Equal(t, userID, chat.UserID)
	body, _ := launchedHook.Payload["body"].(map[string]any)
	assert.Equal(t, "v1.2", body["ref"])
	assertFirings(t, repo, userID, hook.GetId(), map[core.TriggerEventOutcome]int{
		core.TriggerEventLaunched: 1, core.TriggerEventSkipped: 1,
	})

	// --- app-level integration events ------------------------------------
	mine := &core.Connection{
		ID: uuid.NewString(), OwnerKind: core.ConnectionOwnerUser, UserID: userID, IntegrationID: "test",
		AuthKind: "none", Name: "test", ExternalAccountID: strPtr("acct-1"), Status: core.ConnectionStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.Connections().CreateConnection(ctx, mine, nil,
		core.ConnectionEvent{ConnectionID: mine.ID, UserID: userID, Kind: core.ConnectionEventCreated, Actor: "user:" + userID}))
	intDef := base("on issue")
	intDef.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
		Integration: "test", Events: []string{"issues.opened"}, Match: map[string]string{"repository": "acme/app"},
	}}
	createdInt, err := svc.CreateTrigger(authCtx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: intDef}))
	require.NoError(t, err)
	intTrigger := createdInt.Msg.GetTrigger()
	assert.Equal(t, mine.ID, intTrigger.GetConnectionId())

	deliver := func(d webhook.TestDelivery) int {
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/integrations/test/events", strings.NewReader(string(raw)))
		require.NoError(t, err)
		req.Header.Set(webhook.TestSignatureHeader, webhook.SignTestDelivery("e2e-secret", publicURL+"/integrations/test/events", raw))
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	event := func(id, account, repoName string) webhook.TestDelivery {
		return webhook.TestDelivery{ID: id, Account: account, Type: "issues.opened",
			Attributes: map[string]string{"repository": repoName}, Data: map[string]any{"number": float64(7)}}
	}
	// Another user's account, and this user's account on another repo: nothing.
	assert.Equal(t, http.StatusOK, deliver(event("d-other-acct", "acct-2", "acme/app")))
	assert.Equal(t, http.StatusOK, deliver(event("d-other-repo", "acct-1", "acme/other")))
	// The real one, redelivered.
	for i := 0; i < 2; i++ {
		assert.Equal(t, http.StatusOK, deliver(event("d-1", "acct-1", "acme/app")))
	}
	launchedInt := waitForLaunched(t, repo, userID, intTrigger.GetId())
	assert.Equal(t, intTrigger.GetId()+":d-1", launchedInt.DedupeKey)
	assert.Equal(t, "issues.opened", launchedInt.Payload["event"])
	assertFirings(t, repo, userID, intTrigger.GetId(), map[core.TriggerEventOutcome]int{core.TriggerEventLaunched: 1})
}

func waitForLaunched(t *testing.T, repo *db.Repo, userID, triggerID string) *core.TriggerEvent {
	t.Helper()
	launched := core.TriggerEventLaunched
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := repo.GetLatestTriggerEvent(context.Background(), triggerID, &launched)
		if err == nil && ev != nil && ev.ChatID != nil {
			return ev
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("trigger %s: no launched event within 60s", triggerID)
	return nil
}

func assertFirings(t *testing.T, repo *db.Repo, userID, triggerID string, want map[core.TriggerEventOutcome]int) {
	t.Helper()
	// Give a would-be duplicate fire time to (wrongly) land.
	time.Sleep(time.Second)
	events, _, err := repo.ListTriggerEvents(context.Background(), core.TriggerEventFilters{UserID: userID, TriggerID: triggerID, Limit: 50})
	require.NoError(t, err)
	got := map[core.TriggerEventOutcome]int{}
	for _, ev := range events {
		got[ev.Event.Outcome]++
	}
	assert.Equal(t, want, got, "firings of trigger %s", triggerID)
}
