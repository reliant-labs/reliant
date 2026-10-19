// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"testing"
	"time"

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
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A turn served by a routed account must reach the provider driver with the
// same options a turn on the default resolver does. Through the real wiring,
// only the provider's client factory is replaced, by one that records the
// options it is built with.
func TestAccountRouting_RealWiring_RoutedDriverOptionsMatchTheDefaultPath(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	drivers.InitializeAPIKeyProvider(repo)

	prevClient := drivers.AccountAvailabilityClient
	drivers.AccountAvailabilityClient = func(_ context.Context, id models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		return noCatalog{name: string(id)}, nil
	}
	t.Cleanup(func() { drivers.AccountAvailabilityClient = prevClient })

	var captured []llm.DriverOptions
	prevFactory, hadFactory := registry.GetDriverFactory("claude")
	registry.RegisterDriver("claude", func(o *llm.DriverOptions) (registry.Client, error) {
		captured = append(captured, *o)
		return &edgeDriver{apiKey: o.ApiKey}, nil
	})
	t.Cleanup(func() {
		if hadFactory {
			registry.RegisterDriver("claude", prevFactory)
		}
	})

	user := "user-" + uuid.NewString()
	for _, name := range []string{"alice", "bob"} {
		_, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
			UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
			IdentityKey: core.ClaudeIdentityKey(name, "org-"+name), Email: name + "@acme.com",
			ExternalAccountID: name, OrganizationID: "org-" + name,
			Secrets:   core.AccountSecrets{Secret: "access-" + name, Refresh: "refresh-" + name},
			ExpiresAt: time.Now().Add(time.Hour),
		})
		require.NoError(t, err)
	}

	runTurn := func(routed bool) llm.DriverOptions {
		projectID, chatID := "project-"+uuid.NewString(), "chat-"+uuid.NewString()
		require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: user, Name: "p", Path: t.TempDir()}))
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: user}))
		_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID})
		require.NoError(t, err)
		_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: chatID + ":" + chatID + ":0", ThreadID: chatID})
		require.NoError(t, err)
		_, err = repo.SaveMessageToThread(ctx, chatID, chatID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "hello", nil, nil, nil)
		require.NoError(t, err)

		var suite temporaltest.WorkflowTestSuite
		env := suite.NewTestActivityEnvironment()
		activity := handlers.NewCallLLMActivity(repo, nil, nil, emptyConfig{}, nil, nil)
		if routed {
			activity = activity.WithAccountRouting(accountRouting(repo, nil))
		}
		env.RegisterActivity(activity.Execute)
		_, err = env.ExecuteActivity(activity.Execute, types.ActivityInput{
			Runtime: types.RuntimeContext{ChatID: chatID, Thread: chatID},
			Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{
					Literal: &reliantv1.ModelSelector{Id: "claude-5.5-opus@claude"}}},
			}}},
		})
		require.NoError(t, err)
		require.NotEmpty(t, captured, "the provider factory built a driver")
		return captured[len(captured)-1]
	}

	def, ok := models.MustGetRegistry().GetDefinition("claude-5.5-opus")
	require.True(t, ok)
	wantMax := int64(models.EffectiveMaxOutputTokens(def, "claude"))
	require.Positive(t, wantMax)

	def0 := runTurn(false)
	routed := runTurn(true)

	assert.Equal(t, wantMax, def0.MaxTokens, "default path carries the model's max_tokens")
	assert.Equal(t, wantMax, routed.MaxTokens, "routed path carries the model's max_tokens")
	assert.Equal(t, def0.MaxTokens, routed.MaxTokens)
	assert.Equal(t, def0.ReasoningEffort, routed.ReasoningEffort)
	assert.Equal(t, def0.Temperature, routed.Temperature)
	assert.Equal(t, def0.DisableCache, routed.DisableCache)
	assert.Equal(t, def0.Model.DefaultMaxTokens, routed.Model.DefaultMaxTokens)
}
