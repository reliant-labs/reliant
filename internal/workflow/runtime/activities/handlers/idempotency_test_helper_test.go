// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// IdempotencyTestHelper provides a framework for testing activity idempotency
type IdempotencyTestHelper struct {
	t       *testing.T
	env     *testsuite.TestActivityEnvironment
	repo    db.Repository
	sqlDB   *sql.DB
	cleanup func()
}

// NewIdempotencyTestHelper creates a new test helper with a Postgres database
func NewIdempotencyTestHelper(t *testing.T) *IdempotencyTestHelper {
	repo, sqlDB, cleanup := db.SetupTestDBWithRawDB(t)

	// Create Temporal test environment
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()

	return &IdempotencyTestHelper{
		t:       t,
		env:     env,
		repo:    repo,
		sqlDB:   sqlDB,
		cleanup: cleanup,
	}
}

// ExecuteActivity executes an activity with the given input and returns the output.
// If input implements protoInputter (test helper types), it's auto-converted to the proto type.
func (h *IdempotencyTestHelper) ExecuteActivity(activity interface{}, input interface{}, output interface{}) error {
	h.env.RegisterActivity(activity)
	// Auto-convert test helper types to their proto equivalents
	if vi, ok := input.(protoInputter); ok {
		input = vi.protoInput()
	}
	val, err := h.env.ExecuteActivity(activity, input)
	if err != nil {
		return err
	}

	// Decode proto-native outputs and map to legacy test structs where needed.
	switch out := output.(type) {
	case *SaveMessageOutput:
		return val.Get(out)
	case *ExecuteToolsOutput:
		return val.Get(out)
	case *CallLLMOutput:
		return val.Get(out)
	default:
		// Decode directly for callers expecting proto outputs or other types.
		return val.Get(output)
	}
}

// ExecuteActivityWithAttempt executes an activity simulating a specific attempt number
func (h *IdempotencyTestHelper) ExecuteActivityWithAttempt(activity interface{}, input interface{}, attempt int32, output interface{}) error {
	h.env.RegisterActivity(activity)
	// Set the attempt number in activity info
	h.env.SetTestTimeout(0)
	h.env.SetWorkerStopChannel(nil)

	// Execute the activity
	return h.ExecuteActivity(activity, input, output)
}

// Repo returns the repository for database verification
func (h *IdempotencyTestHelper) Repo() db.Repository {
	return h.repo
}

// createMessageWithSeq persists msg with a chat-global seq allocated from the
// database. Fixtures set Ordinal by hand (often reusing the same ordinal
// across threads of one chat), so seq cannot simply mirror it without
// violating UNIQUE(chat_id, seq).
func createMessageWithSeq(ctx context.Context, t *testing.T, repo db.Repository, msg *db.Message) error {
	t.Helper()
	seq, err := repo.GetNextSeq(ctx, msg.ChatID, msg.ThreadID)
	if err != nil {
		return err
	}
	msg.Seq = seq
	return repo.CreateMessage(ctx, msg)
}

// DB returns the underlying SQL database for direct queries
func (h *IdempotencyTestHelper) DB() *sql.DB {
	return h.sqlDB
}

// Cleanup cleans up test resources
func (h *IdempotencyTestHelper) Cleanup() {
	if h.cleanup != nil {
		h.cleanup()
	}
}

// CountMessages counts messages in the database
func (h *IdempotencyTestHelper) CountMessages(ctx context.Context, chatID string) int {
	messages, err := h.repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(h.t, err)
	return len(messages)
}

// CountContentBlocks counts content blocks for a message
func (h *IdempotencyTestHelper) CountContentBlocks(ctx context.Context, messageID string) int {
	blocks, err := h.repo.ListContentBlocks(ctx, messageID)
	require.NoError(h.t, err)
	return len(blocks)
}

// VerifyNoOrphanedRecords checks that no orphaned records exist after retries
func (h *IdempotencyTestHelper) VerifyNoOrphanedRecords(ctx context.Context, chatID string, expectedMessages int) {
	// Verify message count
	actualMessages := h.CountMessages(ctx, chatID)
	require.Equal(h.t, expectedMessages, actualMessages, "Expected %d messages but found %d", expectedMessages, actualMessages)

	// Verify all messages have content blocks
	messages, err := h.repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(h.t, err)

	for _, msg := range messages {
		blocks := h.CountContentBlocks(ctx, msg.ID)
		require.Greater(h.t, blocks, 0, "Message %s has no content blocks", msg.ID)
	}
}

// CreateTestChat creates a test chat for testing.
// Also creates default threads and context windows (required for SaveMessageActivity):
// - Thread with ID = chatID (standard pattern)
// - Thread with ID = "0" (legacy compatibility for tests using Thread: "0")
// Each thread gets an initial context window (sequence 0).
func (h *IdempotencyTestHelper) CreateTestChat(ctx context.Context, chatID, projectID, userID string) *db.Chat {
	chat := &db.Chat{
		ID:        chatID,
		ProjectID: projectID,
		UserID:    userID,
	}
	err := h.repo.CreateChat(ctx, chat)
	require.NoError(h.t, err)

	// Create default thread with ID = chatID (standard pattern)
	_, err = h.repo.CreateThread(ctx, &db.Thread{
		ID:     chatID,
		ChatID: chatID,
	})
	require.NoError(h.t, err)

	// Create context window for the chatID thread
	_, err = h.repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:       chatID + ":" + chatID + ":0",
		ThreadID: chatID,
		Sequence: 0,
	})
	require.NoError(h.t, err)

	// Create legacy "0" thread for backward compatibility with tests using Thread: "0"
	_, err = h.repo.CreateThread(ctx, &db.Thread{
		ID:     "0",
		ChatID: chatID,
	})
	require.NoError(h.t, err)

	// Create context window for the "0" thread
	_, err = h.repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:       chatID + ":0:0",
		ThreadID: "0",
		Sequence: 0,
	})
	require.NoError(h.t, err)

	return chat
}

// CreateTestProject creates a test project.
func (h *IdempotencyTestHelper) CreateTestProject(ctx context.Context, projectID, userID string) *db.Project {
	return h.CreateTestProjectWithPath(ctx, projectID, userID, "/tmp/test")
}

// CreateTestProjectWithPath creates a test project at a specific filesystem path.
func (h *IdempotencyTestHelper) CreateTestProjectWithPath(ctx context.Context, projectID, userID, projectPath string) *db.Project {
	project := &db.Project{
		ID:     projectID,
		UserID: userID,
		Name:   "Test Project",
		Path:   projectPath,
	}
	err := h.repo.CreateProject(ctx, project)
	require.NoError(h.t, err)
	return project
}

// CreateTestWorktree creates a test worktree and attaches it to the chat.
func (h *IdempotencyTestHelper) CreateTestWorktree(ctx context.Context, projectID, chatID, path string) *db.Worktree {
	worktreeID := "wt-" + uuid.New().String()
	branch := "test-branch"
	baseBranch := "main"
	now := time.Now().UTC()
	worktree := &db.Worktree{
		ID:         worktreeID,
		Name:       "Test Worktree",
		Path:       path,
		Branch:     branch,
		BaseBranch: baseBranch,
		ProjectID:  projectID,
		ChatID:     &chatID,
		Status:     1,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	err := h.repo.CreateWorktree(ctx, worktree)
	require.NoError(h.t, err)

	chat, err := h.repo.GetChat(ctx, chatID)
	require.NoError(h.t, err)
	chat.WorktreeID = &worktreeID
	err = h.repo.UpdateChat(ctx, chat)
	require.NoError(h.t, err)

	return worktree
}

// CreateTestWorkflow creates a test workflow for testing
func (h *IdempotencyTestHelper) CreateTestWorkflow(ctx context.Context, workflowID, chatID string) *db.Workflow {
	workflow := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "test-workflow",
		Thread:       "/test",
		Status:       db.Active(),
	}
	err := h.repo.CreateWorkflow(ctx, workflow)
	require.NoError(h.t, err)
	return workflow
}

// CreateTestThreadAndContextWindow creates a thread and context window for testing
// Returns the contextWindowID for use in message creation
// If thread or context window already exists (e.g., from CreateTestChat), ignores the error
func (h *IdempotencyTestHelper) CreateTestThreadAndContextWindow(ctx context.Context, chatID, threadID string) string {
	// Try to create thread (may already exist from CreateTestChat)
	thread := &db.Thread{
		ID:     threadID,
		ChatID: chatID,
	}
	_, _ = h.repo.CreateThread(ctx, thread) // Ignore error - thread may already exist

	// Create context window (may already exist)
	contextWindowID := chatID + ":" + threadID + ":0"
	_, err := h.repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:       contextWindowID,
		ThreadID: threadID,
		Sequence: 0,
	})
	// Ignore unique constraint errors - context window may already exist
	if err != nil && !isUniqueViolation(err) {
		require.NoError(h.t, err)
	}

	return contextWindowID
}

// isUniqueViolation checks if an error is a unique constraint violation (Postgres)
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "duplicate key") || strings.Contains(s, "unique_violation")
}

// CreateTestUserMessage inserts a minimal user message with a text content block
// into the given thread. This satisfies the "non-empty message history" precondition
// required by CallLLMActivity.
func (h *IdempotencyTestHelper) CreateTestUserMessage(ctx context.Context, chatID, threadID string) {
	h.CreateTestUserMessageWithText(ctx, chatID, threadID, "test")
}

// CreateTestUserMessageWithText inserts a user message with custom text content.
func (h *IdempotencyTestHelper) CreateTestUserMessageWithText(ctx context.Context, chatID, threadID, text string) {
	contextWindowID := chatID + ":" + threadID + ":0"
	msgID := uuid.New().String()
	ordinal, err := h.repo.GetNextOrdinal(ctx, threadID)
	require.NoError(h.t, err)
	seq, err := h.repo.GetNextSeq(ctx, chatID, threadID)
	require.NoError(h.t, err)

	err = h.repo.CreateMessage(ctx, &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         ordinal,
		Seq:             seq,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})
	require.NoError(h.t, err)

	err = h.repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: msgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of(text),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(h.t, err)
}

// CreateTestThread creates just the thread (no context window) for testing
// Use this when SaveMessageActivity will create its own context window
func (h *IdempotencyTestHelper) CreateTestThread(ctx context.Context, chatID, threadID string) {
	thread := &db.Thread{
		ID:     threadID,
		ChatID: chatID,
	}
	_, err := h.repo.CreateThread(ctx, thread)
	require.NoError(h.t, err)
}
