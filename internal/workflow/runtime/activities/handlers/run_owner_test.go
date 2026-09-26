// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// These tests drive the activities that create runs, not the store, because
// that is where an owner can be forgotten: a store test that sets OwnerUserID
// itself passes whether or not any real write site does.

func requireOwner(t *testing.T, repo db.Repository, runID, want string) {
	t.Helper()
	run, err := repo.GetWorkflow(context.Background(), runID)
	require.NoError(t, err)
	require.NotNil(t, run)
	require.NotNil(t, run.OwnerUserID, "run %s was created without an owner", runID)
	assert.Equal(t, want, *run.OwnerUserID)
}

// A spawned run gets its owner from its parent run — including when the chat
// it names would say otherwise, which proves the parent is consulted first
// rather than the chat.
func TestCreateWorkflowWithThread_SpawnedRunTakesParentOwner(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	projectID, chatID := uuid.NewString(), uuid.NewString()
	h.CreateTestProject(ctx, projectID, "chat-user")
	h.CreateTestChat(ctx, chatID, projectID, "chat-user")

	parentID := uuid.NewString()
	parentOwner := "parent-owner"
	svc := threads.NewService(h.Repo())
	_, _, _, err := svc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: parentID, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: parentID, Status: db.Active(), CreatedAt: time.Now().UTC(),
			OwnerUserID: &parentOwner,
		},
		ThreadID: parentID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	activity := NewCreateWorkflowWithThreadActivity(svc, h.Repo())
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	childID := uuid.NewString()
	_, err = env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:       childID,
		WorkflowName:     "builtin://agent",
		ParentWorkflowID: &parentID,
		ChatID:           chatID,
		ThreadID:         childID,
	})
	require.NoError(t, err)

	requireOwner(t, h.Repo(), childID, parentOwner)
}

// A run created by the activity with no parent takes its chat's user.
func TestCreateWorkflowWithThread_RootRunTakesChatOwner(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	projectID, chatID := uuid.NewString(), uuid.NewString()
	h.CreateTestProject(ctx, projectID, "chat-user")
	h.CreateTestChat(ctx, chatID, projectID, "chat-user")

	activity := NewCreateWorkflowWithThreadActivity(threads.NewService(h.Repo()), h.Repo())
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	runID := uuid.NewString()
	_, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:   runID,
		WorkflowName: "builtin://agent",
		ChatID:       chatID,
		ThreadID:     runID,
	})
	require.NoError(t, err)

	requireOwner(t, h.Repo(), runID, "chat-user")
}

// WorkflowStatus creates the root run row when a run starts without one, and
// creates a child row when its status write races ahead of the parent's
// CreateWorkflowWithThread. Both rows must carry an owner.
func TestWorkflowStatus_CreatedRunsCarryAnOwner(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	projectID, chatID := uuid.NewString(), uuid.NewString()
	h.CreateTestProject(ctx, projectID, "chat-user")
	h.CreateTestChat(ctx, chatID, projectID, "chat-user")

	activity := NewWorkflowStatusActivity(h.Repo())

	rootID := uuid.NewString()
	var out WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
		ChatID: chatID, WorkflowID: rootID, WorkflowName: "builtin://chat",
		Status: "started", Thread: chatID,
	}, &out))
	requireOwner(t, h.Repo(), rootID, "chat-user")

	childID := uuid.NewString()
	require.NoError(t, h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
		ChatID: chatID, WorkflowID: childID, WorkflowName: "builtin://sub-agent",
		Status: "started", Thread: childID, ParentWorkflowID: rootID,
	}, &out))
	requireOwner(t, h.Repo(), childID, "chat-user")
}
