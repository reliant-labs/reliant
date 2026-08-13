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

func TestCreateWorkflowWithThread_NewThread(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Setup: Create a project and chat
	// Unique project ID per test: the package shares one Postgres DB, so a
	// constant ID would collide (projects_pkey) across test functions.
	projectID := uuid.New().String()
	err := repo.CreateProject(ctx, &db.Project{
		ID:        projectID,
		Name:      "Test Project",
		Path:      "/tmp/test",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	chatID := uuid.New().String()
	err = repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Test Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create threads service and activity
	threadsService := threads.NewService(repo)
	activity := NewCreateWorkflowWithThreadActivity(threadsService)

	// Use Temporal test suite to provide proper activity context
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	workflowID := uuid.New().String()
	threadID := uuid.New().String()
	threadTitle := "Test Thread"

	// Execute the activity
	val, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:   workflowID,
		WorkflowName: "builtin://agent",
		ChatID:       chatID,
		ThreadID:     threadID,
		ThreadTitle:  &threadTitle,
	})

	require.NoError(t, err)
	var result CreateWorkflowWithThreadOutput
	require.NoError(t, val.Get(&result))

	// Verify output
	assert.Equal(t, workflowID, result.WorkflowID)
	assert.Equal(t, threadID, result.ThreadID)
	assert.NotEmpty(t, result.ContextWindowID)

	// Verify workflow was created in DB
	workflow, err := repo.GetWorkflow(ctx, workflowID)
	require.NoError(t, err)
	assert.Equal(t, workflowID, workflow.ID)
	assert.Equal(t, "builtin://agent", workflow.WorkflowName)
	assert.Equal(t, chatID, workflow.ChatID)
	assert.Equal(t, threadID, workflow.Thread)
	assert.Equal(t, db.WorkflowStatusRunning, workflow.Status)

	// Verify thread was created in DB
	thread, err := repo.GetThread(ctx, threadID)
	require.NoError(t, err)
	assert.Equal(t, threadID, thread.ID)
	assert.Equal(t, chatID, thread.ChatID)
	assert.Equal(t, workflowID, *thread.WorkflowID)
}

func TestCreateWorkflowWithThread_ForkedThread(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Setup: Create a project, chat, and parent thread with context window
	// Unique project ID per test: the package shares one Postgres DB, so a
	// constant ID would collide (projects_pkey) across test functions.
	projectID := uuid.New().String()
	err := repo.CreateProject(ctx, &db.Project{
		ID:        projectID,
		Name:      "Test Project",
		Path:      "/tmp/test",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	chatID := uuid.New().String()
	err = repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Test Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent thread using threads service
	threadsService := threads.NewService(repo)
	parentThread, parentCW, err := threadsService.CreateThread(ctx, threads.CreateThreadOpts{
		ChatID: chatID,
	})
	require.NoError(t, err)

	// Create activity
	activity := NewCreateWorkflowWithThreadActivity(threadsService)

	// Use Temporal test suite
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	workflowID := uuid.New().String()
	childThreadID := uuid.New().String()
	threadTitle := "Forked Thread"

	// Execute the activity with fork
	val, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:     workflowID,
		WorkflowName:   "builtin://agent",
		ChatID:         chatID,
		ThreadID:       childThreadID,
		ThreadTitle:    &threadTitle,
		ForkFromThread: &parentThread.ID,
	})

	require.NoError(t, err)
	var result CreateWorkflowWithThreadOutput
	require.NoError(t, val.Get(&result))

	// Verify output
	assert.Equal(t, workflowID, result.WorkflowID)
	assert.Equal(t, childThreadID, result.ThreadID)
	assert.NotEmpty(t, result.ContextWindowID)
	// Forked thread gets a new context window
	assert.NotEqual(t, parentCW.ID, result.ContextWindowID)

	// Verify thread was created with parent reference
	thread, err := repo.GetThread(ctx, childThreadID)
	require.NoError(t, err)
	assert.Equal(t, childThreadID, thread.ID)
	assert.Equal(t, chatID, thread.ChatID)
	assert.NotNil(t, thread.ParentThreadID)
	assert.Equal(t, parentThread.ID, *thread.ParentThreadID)
}

func TestCreateWorkflowWithThread_ChildWorkflow(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Setup: Create a project and chat
	// Unique project ID per test: the package shares one Postgres DB, so a
	// constant ID would collide (projects_pkey) across test functions.
	projectID := uuid.New().String()
	err := repo.CreateProject(ctx, &db.Project{
		ID:        projectID,
		Name:      "Test Project",
		Path:      "/tmp/test",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	chatID := uuid.New().String()
	err = repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Test Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create threads service and activity
	threadsService := threads.NewService(repo)

	// Create parent workflow first (required for FK constraint)
	parentWorkflowID := uuid.New().String()
	parentThreadID := uuid.New().String()
	_, _, _, err = threadsService.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           parentWorkflowID,
			ChatID:       chatID,
			WorkflowName: "builtin://agent",
			Thread:       parentThreadID,
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now().UTC(),
		},
		ThreadID: parentThreadID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	activity := NewCreateWorkflowWithThreadActivity(threadsService)

	// Use Temporal test suite
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	childWorkflowID := uuid.New().String()
	childThreadID := uuid.New().String()
	spawnedByNodeID := "spawn_node_1"
	loopIteration := int64(2)

	// Execute the activity for a child workflow
	val, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:       childWorkflowID,
		WorkflowName:     "builtin://agent",
		ParentWorkflowID: &parentWorkflowID,
		SpawnedByNodeID:  &spawnedByNodeID,
		LoopIteration:    &loopIteration,
		ChatID:           chatID,
		ThreadID:         childThreadID,
	})

	require.NoError(t, err)
	var result CreateWorkflowWithThreadOutput
	require.NoError(t, val.Get(&result))

	// Verify workflow was created with parent reference
	workflow, err := repo.GetWorkflow(ctx, childWorkflowID)
	require.NoError(t, err)
	assert.Equal(t, childWorkflowID, workflow.ID)
	assert.NotNil(t, workflow.ParentID)
	assert.Equal(t, parentWorkflowID, *workflow.ParentID)
	assert.NotNil(t, workflow.SpawnedByNodeID)
	assert.Equal(t, spawnedByNodeID, *workflow.SpawnedByNodeID)
	assert.NotNil(t, workflow.LoopIteration)
	assert.Equal(t, loopIteration, *workflow.LoopIteration)
}

func TestCreateWorkflowWithThread_MissingWorkflowID(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	threadsService := threads.NewService(repo)
	activity := NewCreateWorkflowWithThreadActivity(threadsService)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	// Execute with missing workflow_id
	_, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowName: "builtin://agent",
		ChatID:       "test-chat",
		ThreadID:     "test-thread",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow_id is required")
}

func TestCreateWorkflowWithThread_MissingWorkflowName(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	threadsService := threads.NewService(repo)
	activity := NewCreateWorkflowWithThreadActivity(threadsService)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	// Execute with missing workflow_name
	_, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID: "test-workflow",
		ChatID:     "test-chat",
		ThreadID:   "test-thread",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow_name is required")
}

func TestCreateWorkflowWithThread_MissingChatID(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	threadsService := threads.NewService(repo)
	activity := NewCreateWorkflowWithThreadActivity(threadsService)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	// Execute with missing chat_id
	_, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:   "test-workflow",
		WorkflowName: "builtin://agent",
		ThreadID:     "test-thread",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "chat_id is required")
}

func TestCreateWorkflowWithThread_DefaultThreadID(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Setup: Create a project and chat
	// Unique project ID per test: the package shares one Postgres DB, so a
	// constant ID would collide (projects_pkey) across test functions.
	projectID := uuid.New().String()
	err := repo.CreateProject(ctx, &db.Project{
		ID:        projectID,
		Name:      "Test Project",
		Path:      "/tmp/test",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	chatID := uuid.New().String()
	err = repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Test Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create threads service and activity
	threadsService := threads.NewService(repo)
	activity := NewCreateWorkflowWithThreadActivity(threadsService)

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	workflowID := uuid.New().String()

	// Execute without specifying ThreadID - should use WorkflowID as default
	val, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:   workflowID,
		WorkflowName: "builtin://agent",
		ChatID:       chatID,
		// ThreadID intentionally omitted
	})

	require.NoError(t, err)
	var result CreateWorkflowWithThreadOutput
	require.NoError(t, val.Get(&result))

	// When ThreadID is empty, the threads service will generate a new ID
	// but the workflow.Thread should be set to workflowID
	workflow, err := repo.GetWorkflow(ctx, workflowID)
	require.NoError(t, err)
	assert.Equal(t, workflowID, workflow.Thread)
}
