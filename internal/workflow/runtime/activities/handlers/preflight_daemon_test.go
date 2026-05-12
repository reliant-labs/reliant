// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// mockLocalExecutor is a ToolExecutor that is NOT a RemoteExecutor.
type mockLocalExecutor struct{}

func (m *mockLocalExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	return &toolexec.ToolResult{}, nil
}

func (m *mockLocalExecutor) Close() error { return nil }

func TestPreflightDaemonCheck_NonRemoteExecutor(t *testing.T) {
	// When the tool executor is NOT a RemoteExecutor,
	// the activity should return DaemonAvailable: true immediately.
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	projectID := "test-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:        projectID,
		Name:      "Test Project",
		Path:      "/tmp/test",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Test Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}))

	activity := NewPreflightDaemonCheckActivity(repo, &mockLocalExecutor{})

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	result, err := env.ExecuteActivity(activity.Execute, PreflightDaemonCheckInput{
		ChatID: chatID,
	})
	require.NoError(t, err)

	var output PreflightDaemonCheckOutput
	require.NoError(t, result.Get(&output))
	assert.True(t, output.DaemonAvailable, "daemon should be available with non-remote executor")
}

func TestPreflightDaemonCheck_ChatNotFound(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	activity := NewPreflightDaemonCheckActivity(repo, &mockLocalExecutor{})

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	_, err := env.ExecuteActivity(activity.Execute, PreflightDaemonCheckInput{
		ChatID: "nonexistent-chat-id",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get chat")
}
