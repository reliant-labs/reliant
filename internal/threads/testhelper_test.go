package threads

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// testHelper wraps db.Repo and provides convenience methods for testing.
// It uses a real in-memory SQLite database instead of mocks.
type testHelper struct {
	t    *testing.T
	repo *db.Repo
	svc  *Service

	// Track created entities for cleanup/reference
	chatID    string
	projectID string
	cleanup   func()
}

// newTestHelper creates a new test helper with an in-memory database.
func newTestHelper(t *testing.T) *testHelper {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)

	h := &testHelper{
		t:         t,
		repo:      repo,
		projectID: "test-project", // Created by SetupTestDB
		cleanup:   cleanup,
	}

	// Create a default chat for tests
	h.chatID = h.createChat("test-chat")

	h.svc = NewService(repo)
	return h
}

// Close cleans up the test database.
func (h *testHelper) Close() {
	h.cleanup()
}

// createChat creates a chat with the given ID or generates one.
func (h *testHelper) createChat(id string) string {
	h.t.Helper()
	if id == "" {
		id = uuid.New().String()
	}

	ctx := context.Background()
	chat := &db.Chat{
		ID:        id,
		ProjectID: h.projectID,
		Title:     "Test Chat",
	}
	if err := h.repo.CreateChat(ctx, chat); err != nil {
		h.t.Fatalf("failed to create chat: %v", err)
	}
	return id
}

// createThread creates a thread using the service.
func (h *testHelper) createThread(id, conversationID string) (*db.Thread, *db.ContextWindow) {
	h.t.Helper()
	ctx := context.Background()

	thread, cw, err := h.svc.CreateThread(ctx, CreateThreadOpts{
		ID:             id,
		ConversationID: conversationID,
	})
	if err != nil {
		h.t.Fatalf("failed to create thread: %v", err)
	}
	return thread, cw
}

// forkThread creates a forked thread using the service.
func (h *testHelper) forkThread(id, conversationID, parentThreadID string, forkAtOrdinal int64, forkAtCWID string) (*db.Thread, *db.ContextWindow) {
	h.t.Helper()
	ctx := context.Background()

	thread, cw, err := h.svc.ForkThread(ctx, ForkThreadOpts{
		ID:                    id,
		ConversationID:        conversationID,
		ParentThreadID:        parentThreadID,
		ForkAtOrdinal:         forkAtOrdinal,
		ForkAtContextWindowID: forkAtCWID,
	})
	if err != nil {
		h.t.Fatalf("failed to fork thread: %v", err)
	}
	return thread, cw
}

// compact performs compaction on a thread.
func (h *testHelper) compact(threadID, summaryMessageID string) *db.ContextWindow {
	h.t.Helper()
	ctx := context.Background()

	cw, err := h.svc.Compact(ctx, threadID, summaryMessageID)
	if err != nil {
		h.t.Fatalf("failed to compact thread: %v", err)
	}
	return cw
}

// addMessageWithID adds a message with a specific ID.
func (h *testHelper) addMessageWithID(id, chatID, threadID, contextWindowID string, ordinal int64, role int32) *db.Message {
	h.t.Helper()
	ctx := context.Background()

	msg := &db.Message{
		ID:              id,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Ordinal:         ordinal,
		Role:            reliantv1.MessageRole(role),
		CreatedAt:       time.Now(),
	}
	if err := h.repo.CreateMessage(ctx, msg); err != nil {
		h.t.Fatalf("failed to create message: %v", err)
	}
	return msg
}

// addMessageWithTokens adds a message with token counts.
// The inputTokens and outputTokens params are summed together into TokenCount for backwards compat.
func (h *testHelper) addMessageWithTokens(chatID, threadID, contextWindowID string, ordinal int64, role int32, inputTokens, outputTokens int) *db.Message {
	h.t.Helper()
	ctx := context.Background()

	tc := inputTokens + outputTokens
	msg := &db.Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Ordinal:         ordinal,
		Role:            reliantv1.MessageRole(role),
		TokenCount:      &tc,
		CreatedAt:       time.Now(),
	}
	if err := h.repo.CreateMessage(ctx, msg); err != nil {
		h.t.Fatalf("failed to create message: %v", err)
	}
	return msg
}

// createAttachment creates an attachment in the database.
func (h *testHelper) createAttachment(id, filename, attachmentType string) *db.Attachment {
	h.t.Helper()
	ctx := context.Background()

	att := &db.Attachment{
		ID:             id,
		Filename:       filename,
		AttachmentType: attachmentType,
		CreatedAt:      time.Now(),
	}
	if err := h.repo.CreateAttachment(ctx, att); err != nil {
		h.t.Fatalf("failed to create attachment: %v", err)
	}
	return att
}
