// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A revoked API key (401) takes its account out of rotation in the real store,
// and the next attempt of the turn is served by the user's other key.
func TestAccountRouting_RealWiring_RejectedAPIKeyLeavesRotation(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	drivers.InitializeAPIKeyProvider(repo)

	prevClient := drivers.AccountAvailabilityClient
	drivers.AccountAvailabilityClient = func(_ context.Context, id models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		return noCatalog{name: string(id)}, nil
	}
	t.Cleanup(func() { drivers.AccountAvailabilityClient = prevClient })

	user := "user-" + uuid.NewString()
	addKey := func(name string) core.ProviderAccount {
		a, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
			UserID: user, Provider: "anthropic", AuthKind: core.AuthKindAPIKey,
			IdentityKey: core.APIKeyIdentityKey(user, "anthropic", "sk-ant-"+name),
			Email:       name + "@acme.com", Secrets: core.AccountSecrets{Secret: "sk-ant-" + name},
		})
		require.NoError(t, err)
		return a
	}
	revoked, good := addKey("revoked"), addKey("good")

	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	unauthorized := fmt.Errorf("stream llm response: %w", &anthropic.Error{StatusCode: 401, Request: req, Response: &http.Response{StatusCode: 401}})

	var servedBy []string
	prevFor := accountDriverFor
	accountDriverFor = func(_ context.Context, _ string, cfg models.DriverConfig, _ models.Model, _ ...llm.DriverOption) (llm.Driver, error) {
		servedBy = append(servedBy, cfg.AccountID)
		return &keyEdge{apiKey: cfg.APIKey, fail: cfg.AccountID == revoked.ID, err: unauthorized}, nil
	}
	t.Cleanup(func() { accountDriverFor = prevFor })

	routing := accountRouting(repo, nil)
	projectID, chatID := "project-"+uuid.NewString(), "chat-"+uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: user, Name: "p", Path: t.TempDir()}))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: user}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: chatID + ":" + chatID + ":0", ThreadID: chatID})
	require.NoError(t, err)
	_, err = repo.SaveMessageToThread(ctx, chatID, chatID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "hello", nil, nil, nil)
	require.NoError(t, err)

	applied, err := repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: chatID, Provider: "anthropic", AccountID: revoked.ID, AssignedAt: time.Now(), Reason: "initial"}, "")
	require.NoError(t, err)
	require.True(t, applied)

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	activity := handlers.NewCallLLMActivity(repo, nil, nil, emptyConfig{}, nil, nil).WithAccountRouting(routing)
	env.RegisterActivity(activity.Execute)
	input := types.ActivityInput{
		Runtime: types.RuntimeContext{ChatID: chatID, Thread: chatID},
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{
				Literal: &reliantv1.ModelSelector{Id: "claude-5.5-opus@anthropic"}}},
		}}},
	}

	// Turn 1: the first key is refused. The failure is a failover, not a dead end.
	_, err = env.ExecuteActivity(activity.Execute, input)
	require.Error(t, err)
	assert.Equal(t, []string{revoked.ID}, servedBy)
	assert.Contains(t, err.Error(), "RELIANT_PROVIDER_USAGE_LIMIT", "reported as a failover the workflow retries at once")
	stored, err := repo.ListProviderAccounts(ctx, user)
	require.NoError(t, err)
	status := map[string]core.AccountStatus{}
	for i := range stored {
		status[stored[i].ID] = stored[i].Status(nil, time.Now())
	}
	assert.Equal(t, core.AccountNeedsSignIn, status[revoked.ID], "the rejected key is out of rotation")
	assert.Equal(t, core.AccountAvailable, status[good.ID])

	// Turn 2: the other key serves.
	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)
	var out reliantv1.CallLLMOutput
	require.NoError(t, val.Get(&out))
	assert.Equal(t, good.ID, out.GetProviderAccountId())
	assert.Contains(t, out.GetResponseText(), "ok from sk-ant-good")
}

// keyEdge answers like a provider that has revoked one key.
type keyEdge struct {
	apiKey string
	fail   bool
	err    error
}

func (d *keyEdge) Name() string                      { return "anthropic" }
func (d *keyEdge) Model() models.Model               { return models.Model{ID: "claude-5.5-opus"} }
func (d *keyEdge) ValidateKey(context.Context) error { return nil }
func (d *keyEdge) SendMessages(ctx context.Context, p []string, m []message.Message, t []tools.Tool) (*llm.DriverResponse, error) {
	return nil, nil
}
func (d *keyEdge) StreamResponse(ctx context.Context, p []string, m []message.Message, t []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 4)
	if d.fail {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: d.err}
		close(ch)
		return ch
	}
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "ok from " + d.apiKey}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "ok from " + d.apiKey, FinishReason: "end_turn", Usage: llm.TokenUsage{TokenCount: 10}}}
	close(ch)
	return ch
}
