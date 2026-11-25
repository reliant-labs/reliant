package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chatupdates"
	"github.com/reliant-labs/reliant/internal/daemonmirror"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The worker is where the planning and worktree tools actually execute. Its
// tools factory takes the chatupdates service as WorkerToolsFactoryDeps.Updates
// and hands it to the tools as their Refetcher; a worker that stops injecting
// it would let every plan, task and worktree write go unannounced while each
// tool's own tests stay green.
func TestWorkerToolsFactoryAnnouncesPlanWritesThroughUpdates(t *testing.T) {
	t.Parallel()
	repo := db.NewTestRepo(t)
	lazy, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1", Namespace: "reliant"})
	require.NoError(t, err)
	t.Cleanup(lazy.Close)

	var mu sync.Mutex
	var announced []string
	updates := &chatupdates.MockService{
		EmitChatRefetchFunc: func(_ context.Context, chatID string, typ core.RefetchType) error {
			mu.Lock()
			defer mu.Unlock()
			announced = append(announced, chatID+"/"+string(typ))
			return nil
		},
	}

	factory, err := NewWorkerToolsFactory(WorkerToolsFactoryDeps{
		DB:            repo,
		Updates:       updates,
		Mirror:        &daemonmirror.MockService{},
		Triggers:      &WorkerTrigger{},
		Credentials:   &WorkerIntegrationCredentials{},
		CatalogSearch: &WorkerCatalogSearch{},
		Router:        &WorkerDaemonRouter{NATSDaemonRouter: &toolexec.NATSDaemonRouter{}},
		Temporal:      &TemporalClient{Client: lazy, Namespace: "reliant"},
		Logger:        slog.New(slog.DiscardHandler),
		Config:        &config.Config{},
	})
	require.NoError(t, err)

	chatID := uuid.NewString()
	now := time.Now()
	require.NoError(t, repo.CreateChat(context.Background(), &db.Chat{
		ID: chatID, Title: "t", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	rc := rctx.NewToolContext(ctx, chatID, chatID, nil, nil)

	input, err := json.Marshal(tools.CreatePlanParams{
		Title: "w", Description: "worker", Complexity: "simple", Tasks: []tools.TaskInput{{Title: "A"}},
	})
	require.NoError(t, err)
	resp, err := factory.CreatePlan().Run(rc, tools.ToolCall{ID: "call-1", Name: "create_plan", Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{chatID + "/" + string(core.RefetchPlanTasks)}, announced,
		"the worker's create_plan announces once, through WorkerToolsFactoryDeps.Updates")
}
