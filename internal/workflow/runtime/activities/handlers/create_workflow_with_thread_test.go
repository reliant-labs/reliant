// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)

	// Use Temporal test suite to provide proper activity context
	suite := &temporaltest.WorkflowTestSuite{}
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
	assert.Equal(t, db.Active(), workflow.Status)

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
	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)

	// Use Temporal test suite
	suite := &temporaltest.WorkflowTestSuite{}
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
			Status:       db.Active(),
			CreatedAt:    time.Now().UTC(),
		},
		ThreadID: parentThreadID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)

	// Use Temporal test suite
	suite := &temporaltest.WorkflowTestSuite{}
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
	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)

	suite := &temporaltest.WorkflowTestSuite{}
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
	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)

	suite := &temporaltest.WorkflowTestSuite{}
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
	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)

	suite := &temporaltest.WorkflowTestSuite{}
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

// A spawned sub-agent's thread must be announced to the UI the moment it is
// created — before any message on it can exist.
//
// The parent creates the child's thread here, then immediately saves the
// child's seed message (initChildWorkflow, step 2). The thread used to be
// announced only later, when the CHILD ran its own WorkflowStatus("started").
// Measured on the dev database: in all 40 of the newest spawns, the first
// message's chat_update preceded the thread announcement. A client receiving
// that message has no thread record and (for a brand-new spawn) no tree row
// either, so InterleavedTimeline could not classify the thread and dropped its
// messages until the next execution-tree refetch landed.
func TestCreateWorkflowWithThread_AnnouncesSpawnThreadAtCreation(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	projectID := uuid.New().String()
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

	threadsService := threads.NewService(repo)
	parentWorkflowID := uuid.New().String()
	_, _, _, err := threadsService.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           parentWorkflowID,
			ChatID:       chatID,
			WorkflowName: "builtin://agent",
			Thread:       chatID,
			Status:       db.Active(),
			CreatedAt:    time.Now().UTC(),
		},
		ThreadID: chatID,
		ChatID:   chatID,
		Origin:   db.ThreadOriginMain,
	})
	require.NoError(t, err)

	before, err := repo.GetLatestUpdateSequence(ctx, chatID)
	require.NoError(t, err)

	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)
	suite := &temporaltest.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	childWorkflowID := uuid.New().String()
	childThreadID := uuid.New().String()
	title := "Fix QA findings"
	origin := db.ThreadOriginSpawn
	originNodeID := "spawn-toolu_test"
	_, err = env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:       childWorkflowID,
		WorkflowName:     "builtin://agent",
		ParentWorkflowID: &parentWorkflowID,
		ChatID:           chatID,
		ThreadID:         childThreadID,
		ThreadTitle:      &title,
		ParentThread:     &chatID,
		Origin:           &origin,
		OriginNodeID:     &originNodeID,
	})
	require.NoError(t, err)

	updates, err := repo.GetUpdatesSince(ctx, chatID, before, 100)
	require.NoError(t, err)

	var announcement map[string]interface{}
	for _, update := range updates {
		if update.UpdateType != db.UpdateTypeThread {
			continue
		}
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(update.Data, &data))
		if data["thread"] == childThreadID {
			announcement = data
			// Keyed like every other thread announcement, so the snapshot's
			// per-thread dedup and the stream's merge treat it as one record.
			assert.Equal(t, childWorkflowID, update.EntityID)
		}
	}
	require.NotNil(t, announcement, "creating a spawn thread must announce it to the chat stream")
	assert.Equal(t, "thread", announcement["update_type"])
	assert.Equal(t, "spawn", announcement["origin"], "the origin is what lets the UI classify the thread")
	assert.Equal(t, childWorkflowID, announcement["workflow_id"])
	assert.Equal(t, title, announcement["thread_title"])
	assert.Equal(t, originNodeID, announcement["origin_node_id"])
	assert.Equal(t, "running", announcement["status"])
}

// The main thread is the chat itself; the UI classifies it by identity, and
// the root workflow has its own announcement path. Announcing it here would be
// noise in every chat's update log.
func TestCreateWorkflowWithThread_DoesNotAnnounceRootThread(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	projectID := uuid.New().String()
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

	threadsService := threads.NewService(repo)
	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)
	suite := &temporaltest.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	_, err := env.ExecuteActivity(activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:   uuid.New().String(),
		WorkflowName: "builtin://agent",
		ChatID:       chatID,
		ThreadID:     chatID,
	})
	require.NoError(t, err)

	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 100)
	require.NoError(t, err)
	for _, update := range updates {
		assert.NotEqual(t, db.UpdateTypeThread, update.UpdateType, "a root workflow's thread is not announced here")
	}
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
	activity := NewCreateWorkflowWithThreadActivity(threadsService, repo)

	suite := &temporaltest.WorkflowTestSuite{}
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
