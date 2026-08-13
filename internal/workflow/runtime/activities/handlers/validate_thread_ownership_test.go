// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func TestValidateThreadOwnership_ValidOwnership(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Setup: Create a project, chat, thread
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

	threadID := uuid.New().String()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID:        threadID,
		ChatID:    chatID, // Thread belongs to this chat
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Test: Validate that thread belongs to the correct chat
	activity := NewValidateThreadOwnershipActivity(repo)

	// Use Temporal test suite to provide proper activity context
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	val, err := env.ExecuteActivity(activity.Execute, ValidateThreadOwnershipInput{
		ThreadID:       threadID,
		ExpectedChatID: chatID,
	})

	require.NoError(t, err)
	var result ValidateThreadOwnershipOutput
	require.NoError(t, val.Get(&result))
	assert.True(t, result.Valid, "Thread should belong to the chat")
	assert.Empty(t, result.ErrorMessage)
}

func TestValidateThreadOwnership_InvalidOwnership(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Setup: Create a project, two chats, and a thread in chat A
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

	chatA := uuid.New().String()
	err = repo.CreateChat(ctx, &db.Chat{
		ID:        chatA,
		UserID:    "test-user",
		Title:     "Chat A",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	chatB := uuid.New().String()
	err = repo.CreateChat(ctx, &db.Chat{
		ID:        chatB,
		UserID:    "test-user",
		Title:     "Chat B (branched)",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Thread belongs to Chat A
	threadInChatA := uuid.New().String()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID:        threadInChatA,
		ChatID:    chatA, // Thread belongs to chat A
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Test: Try to validate thread from Chat A against Chat B (should fail)
	activity := NewValidateThreadOwnershipActivity(repo)

	// Use Temporal test suite to provide proper activity context
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	val, err := env.ExecuteActivity(activity.Execute, ValidateThreadOwnershipInput{
		ThreadID:       threadInChatA,
		ExpectedChatID: chatB, // Wrong chat!
	})

	require.NoError(t, err)
	var result ValidateThreadOwnershipOutput
	require.NoError(t, val.Get(&result))
	assert.False(t, result.Valid, "Thread should NOT belong to Chat B")
	assert.Contains(t, result.ErrorMessage, "Cannot resume thread")
	assert.Contains(t, result.ErrorMessage, "different conversation")
}

func TestValidateThreadOwnership_ThreadNotFound(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Test: Validate a non-existent thread
	activity := NewValidateThreadOwnershipActivity(repo)

	// Use Temporal test suite to provide proper activity context
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	val, err := env.ExecuteActivity(activity.Execute, ValidateThreadOwnershipInput{
		ThreadID:       "non-existent-thread",
		ExpectedChatID: "some-chat",
	})

	require.NoError(t, err)
	var result ValidateThreadOwnershipOutput
	require.NoError(t, val.Get(&result))
	assert.False(t, result.Valid, "Non-existent thread should fail validation")
	assert.Contains(t, result.ErrorMessage, "Thread not found")
}

func TestValidateThreadOwnership_SubAgentThread(t *testing.T) {
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()

	// Setup: Create a project, chat, root thread, and sub-agent thread
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

	// Root thread
	rootThreadID := uuid.New().String()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID:        rootThreadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Sub-agent thread (spawned from root thread, same chat)
	subAgentThreadID := uuid.New().String()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID:             subAgentThreadID,
		ChatID:         chatID, // Same chat as root
		ParentThreadID: &rootThreadID,
		CreatedAt:      time.Now(),
	})
	require.NoError(t, err)

	// Test: Validate that sub-agent thread belongs to the same chat
	activity := NewValidateThreadOwnershipActivity(repo)

	// Use Temporal test suite to provide proper activity context
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(activity.Execute)

	val, err := env.ExecuteActivity(activity.Execute, ValidateThreadOwnershipInput{
		ThreadID:       subAgentThreadID,
		ExpectedChatID: chatID,
	})

	require.NoError(t, err)
	var result ValidateThreadOwnershipOutput
	require.NoError(t, val.Get(&result))
	assert.True(t, result.Valid, "Sub-agent thread should belong to the same chat")
	assert.Empty(t, result.ErrorMessage)
}
