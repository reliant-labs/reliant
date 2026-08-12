package threads

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// testHelper wraps db.Repo and provides convenience methods for testing.
// It uses a real Postgres database (via SetupTestDB) instead of mocks.
type testHelper struct {
	t    *testing.T
	repo *db.Repo
	svc  *Service

	// Track created entities for cleanup/reference
	chatID    string
	projectID string
	cleanup   func()
}

// newTestHelper creates a new test helper with a test database.
func newTestHelper(t *testing.T) *testHelper {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)

	h := &testHelper{
		t:         t,
		repo:      repo,
		projectID: "test-project", // Created by SetupTestDB
		cleanup:   cleanup,
	}

	// Create a default chat for tests. Use a generated ID (empty -> uuid) rather
	// than a constant: the whole package shares one Postgres DB, so a fixed chat
	// ID would collide (chats_pkey) across every test function that builds a helper.
	h.chatID = h.createChat("")

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
func (h *testHelper) createThread(id, chatID string) (*db.Thread, *db.ContextWindow) {
	h.t.Helper()
	ctx := context.Background()

	thread, cw, err := h.svc.CreateThread(ctx, CreateThreadOpts{
		ID:     id,
		ChatID: chatID,
	})
	if err != nil {
		h.t.Fatalf("failed to create thread: %v", err)
	}
	return thread, cw
}

// forkThread creates a forked thread using the service. forkAtOrdinal is a
// position in forkAtCWID's messages, resolved here to the message it names
// (or nil when forkAtOrdinal < 0, matching the old "empty parent" convention)
// -- callers pass ordinals because most fixtures already track ordinals for
// message identity, and the resolution is exactly what
// 20260803010000_fork_points_reference_messages.sql's backfill does.
func (h *testHelper) forkThread(id, chatID, parentThreadID string, forkAtOrdinal int64, forkAtCWID string) (*db.Thread, *db.ContextWindow) {
	h.t.Helper()
	ctx := context.Background()

	forkAtMessageID := h.messageIDAtOrdinal(forkAtCWID, forkAtOrdinal)

	thread, cw, err := h.svc.ForkThread(ctx, ForkThreadOpts{
		ID:                    id,
		ChatID:                chatID,
		ParentThreadID:        parentThreadID,
		ForkAtContextWindowID: forkAtCWID,
		ForkAtMessageID:       forkAtMessageID,
	})
	if err != nil {
		h.t.Fatalf("failed to fork thread: %v", err)
	}
	return thread, cw
}

// messageIDAtOrdinal resolves the message with the given ordinal in the given
// context window, or nil if forkAtOrdinal < 0 or no such message exists.
func (h *testHelper) messageIDAtOrdinal(contextWindowID string, forkAtOrdinal int64) *string {
	h.t.Helper()
	if forkAtOrdinal < 0 {
		return nil
	}
	ctx := context.Background()
	msgs, err := h.repo.GetMessagesByContextWindow(ctx, contextWindowID, nil)
	if err != nil {
		h.t.Fatalf("failed to list messages for fork resolution: %v", err)
	}
	for _, m := range msgs {
		if m.Ordinal == forkAtOrdinal {
			id := m.ID
			return &id
		}
	}
	return nil
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

	seq, err := h.repo.GetNextSeq(ctx, chatID, threadID)
	if err != nil {
		h.t.Fatalf("failed to get next seq: %v", err)
	}

	msg := &db.Message{
		ID:              id,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Ordinal:         ordinal,
		Seq:             seq,
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

	seq, err := h.repo.GetNextSeq(ctx, chatID, threadID)
	if err != nil {
		h.t.Fatalf("failed to get next seq: %v", err)
	}

	tc := inputTokens + outputTokens
	msg := &db.Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Ordinal:         ordinal,
		Seq:             seq,
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
