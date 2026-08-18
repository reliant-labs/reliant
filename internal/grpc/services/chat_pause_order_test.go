// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
)

// orderRecordingPauseController wraps *fakeDaemonRouter-style order recording
// for the workflow-pause signal side, sharing the same log with the daemon
// cancel push so a test can assert relative order.
type orderRecordingPauseController struct {
	mu  *sync.Mutex
	log *[]string
}

func (p orderRecordingPauseController) PauseWorkflow(_ context.Context, _, _, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.log = append(*p.log, "pause-signal")
	return nil
}

func (p orderRecordingPauseController) ResumeWorkflow(context.Context, string, string) error {
	return nil
}

func (p orderRecordingPauseController) ResumeInterruptedWorkflow(context.Context, string, string) (string, error) {
	return "", nil
}

func (p orderRecordingPauseController) SignalWithRecovery(context.Context, string, string, interface{}) error {
	return nil
}

// orderRecordingDaemonRouter embeds fakeDaemonRouter and overrides only the
// cancel push to append into the shared order log.
type orderRecordingDaemonRouter struct {
	*fakeDaemonRouter
	mu  *sync.Mutex
	log *[]string
}

func (r orderRecordingDaemonRouter) SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error {
	r.mu.Lock()
	*r.log = append(*r.log, "cancel:"+requestID)
	r.mu.Unlock()
	return r.fakeDaemonRouter.SendToolExecutionCancel(ctx, userID, requestID, reason)
}

// TestPauseChat_CancelsToolsBeforeSignalingWorkflow pins the fast-cancel
// ordering fix for the pause verb, mirroring
// TestInterruptThread_CancelsToolsBeforeSignalingWorkflow. Pause and
// interrupt must differ only in scope, so both must push the daemon cancel
// before freeing the workflow to move on.
func TestPauseChat_CancelsToolsBeforeSignalingWorkflow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	projectID := "test-project-pause-order-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Pause Order Test",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.NewString()
	rootThreadID := chatID
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "Test Chat", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &rootThreadID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: rootThreadID, ChatID: chatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning, CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID: uuid.NewString(), ThreadID: rootThreadID, Sequence: 0, CreatedAt: now,
	})
	require.NoError(t, err)

	callID := "toolu_" + uuid.NewString()
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID:          callID,
		ChatID:      chatID,
		ThreadID:    &rootThreadID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: now,
		StartedAt:   &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	var mu sync.Mutex
	var order []string
	pauseController := orderRecordingPauseController{mu: &mu, log: &order}
	daemonRouter := orderRecordingDaemonRouter{fakeDaemonRouter: &fakeDaemonRouter{}, mu: &mu, log: &order}

	service := &ChatService{
		database: repo,
		runs:     runs.NewService(repo, nil, pauseController),
		threads: threads.NewService(repo,
			threads.WithToolCanceler(daemonRouter),
		),
	}

	resp, err := service.PauseChat(ctx, connect.NewRequest(&reliantv1.PauseChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	require.Equal(t, []string{"cancel:" + callID, "pause-signal"}, order,
		"the daemon cancel must be pushed BEFORE the workflow pause signal -- "+
			"signalling first frees the workflow to move on while the in-flight tool is still alive")
}
