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
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The production wiring, end to end: stored accounts -> drivers.GetAvailableDrivers
// -> accountRouting (real router over the real store, real accountlimits
// Recorder) -> the real CallLLMActivity. Only the network edge is faked: the
// driver the real drivers.GetDriverForAccount builds is replaced by one that
// answers (or is refused) without a provider.

type noCatalog struct{ name string }

func (c noCatalog) Name() string { return c.name }
func (noCatalog) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, nil
}
func (noCatalog) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	return nil
}
func (noCatalog) ValidateKey(context.Context) error { return nil }

// edgeDriver is the network edge of one account.
type edgeDriver struct {
	apiKey string
	refuse *ratelimit.UsageLimitError
}

func (d *edgeDriver) Name() string        { return "claude" }
func (d *edgeDriver) Model() models.Model { return models.Model{ID: "claude-5.5-opus"} }
func (d *edgeDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, nil
}
func (d *edgeDriver) ValidateKey(context.Context) error { return nil }
func (d *edgeDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 4)
	if d.refuse != nil {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: d.refuse}
		close(ch)
		return ch
	}
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "ok from " + d.apiKey}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "ok from " + d.apiKey, FinishReason: "end_turn", Usage: llm.TokenUsage{TokenCount: 10}}}
	close(ch)
	return ch
}

type emptyConfig struct{}

func (emptyConfig) GetProjectConfig(context.Context, config.ProjectRef) (*config.Config, error) {
	return &config.Config{}, nil
}

func TestAccountRouting_RealWiring_FailsOverToTheSecondAccountWithANotice(t *testing.T) {
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
	addAccount := func(name string) core.ProviderAccount {
		a, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
			UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
			IdentityKey: core.ClaudeIdentityKey(name, "org-"+name), Email: name + "@acme.com",
			ExternalAccountID: name, OrganizationID: "org-" + name,
			Secrets:   core.AccountSecrets{Secret: "access-" + name, Refresh: "refresh-" + name},
			ExpiresAt: time.Now().Add(time.Hour),
		})
		require.NoError(t, err)
		return a
	}
	alice, bob := addAccount("alice"), addAccount("bob")

	// Production wiring, with one change: the driver the real
	// drivers.GetDriverForAccount builds for an account is wrapped, so the
	// request never leaves the process. The real builder still runs, so the
	// account's credential reaching it is checked too.
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	var builtFor []string
	prevFor := accountDriverFor
	accountDriverFor = func(ctx context.Context, userID string, cfg models.DriverConfig, m models.Model, opts ...llm.DriverOption) (llm.Driver, error) {
		_, err := drivers.GetDriverForAccount(ctx, userID, cfg, m, opts...)
		require.NoError(t, err, "the real per-account driver builds from the account's own credential")
		builtFor = append(builtFor, cfg.AccountID)
		d := &edgeDriver{apiKey: cfg.APIKey}
		if cfg.AccountID == alice.ID {
			d.refuse = &ratelimit.UsageLimitError{Provider: "Claude subscription", StatusCode: 429, ResetAt: reset, Window: "five_hour"}
		}
		return d, nil
	}
	t.Cleanup(func() { accountDriverFor = prevFor })

	recorder := accountlimits.NewRecorder(repo, nil)
	defer recorder.Close()
	routing := accountRouting(repo, recorder)
	require.NotNil(t, routing.DriverFor, "with GetDriverForAccount wired, routing is live")

	// A chat with one user message, whose thread is already on alice.
	projectID, chatID := "project-"+uuid.NewString(), "chat-"+uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: user, Name: "p", Path: t.TempDir()}))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: user}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: chatID + ":" + chatID + ":0", ThreadID: chatID})
	require.NoError(t, err)
	_, err = repo.SaveMessageToThread(ctx, chatID, chatID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "hello", nil, nil, nil)
	require.NoError(t, err)
	applied, err := repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: chatID, Provider: "claude", AccountID: alice.ID, AssignedAt: time.Now(), Reason: "initial"}, "")
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
				Literal: &reliantv1.ModelSelector{Id: "claude-5.5-opus@claude"}}},
		}}},
	}

	// Turn 1: alice is refused; she is benched (real Recorder, real store).
	_, err = env.ExecuteActivity(activity.Execute, input)
	require.Error(t, err)
	assert.Equal(t, []string{alice.ID}, builtFor, "the thread's sticky account served the request")
	limits, err := repo.ListAccountLimits(ctx, user)
	require.NoError(t, err)
	require.NotNil(t, limits[alice.ID].LimitedUntil, "alice is benched in the store")
	assert.WithinDuration(t, reset, *limits[alice.ID].LimitedUntil, time.Second)
	assert.Nil(t, limits[bob.ID].LimitedUntil, "bob is untouched")

	// Turn 2 (the workflow's immediate retry): the thread continues on bob.
	val, err := env.ExecuteActivity(activity.Execute, input)
	require.NoError(t, err)
	var out reliantv1.CallLLMOutput
	require.NoError(t, val.Get(&out))
	assert.Equal(t, bob.ID, out.GetProviderAccountId())
	assert.Equal(t, bob.ID, builtFor[len(builtFor)-1])
	assert.Contains(t, out.GetResponseText(), "ok from access-bob", "bob's own credential served it")
	a, err := repo.GetThreadAssignment(ctx, chatID, "claude")
	require.NoError(t, err)
	assert.Equal(t, bob.ID, a.AccountID, "and the assignment moved")

	// One notice, in the thread.
	msgs, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	var notices []string
	for _, m := range msgs {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		blocks, err := repo.ListContentBlocks(ctx, m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil {
				notices = append(notices, *b.Content)
			}
		}
	}
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0], "Claude account **alice@acme.com** reached its 5-hour limit")
	assert.Contains(t, notices[0], "Continuing on **bob@acme.com**.")
}
