package messagewrite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/messagewrite"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
)

// The existing facade treats every window lookup error as absence. This test
// records that behavior, not an endorsement of masking transient errors.
func TestWindowLookupErrorMatchesExistingCreateAttempt(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	chatID := "messagewrite-window-parity"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "test-user", ProjectID: "test-project", CreatedAt: now, UpdatedAt: now, LastActive: now}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	o := repo.ORM()
	windows := store.NewContextWindowExtStore(o)
	transient := errors.New("injected window read error")
	var reads, creates int
	windowMock := &messagewrite.MockWindows{
		GetMaxSequenceForThreadFunc: windows.GetMaxSequenceForThread,
		GetContextWindowFunc: func(context.Context, string) (*core.ContextWindow, error) {
			reads++
			return nil, transient
		},
		InsertContextWindowFunc: func(ctx context.Context, cw *core.ContextWindow) (*core.ContextWindow, error) {
			creates++
			return windows.InsertContextWindow(ctx, cw)
		},
	}
	svc, err := messagewrite.New(messagewrite.Deps{Tx: repo, Order: store.NewMessageOrderCounterExtStore(o), Messages: store.NewMessageExtStore(o), Windows: windowMock, Threads: store.NewThreadExtStore(o), Workflows: store.NewWorkflowExtStore(o), Blocks: store.NewMessageContentBlockExtStore(o), Attachments: store.NewAttachmentExtStore(o), Updates: repo})
	require.NoError(t, err)
	_, err = svc.SaveMessageToThread(ctx, chatID, chatID, int32(pb.MessageRole_MESSAGE_ROLE_USER), "hello", nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, reads)
	require.Equal(t, 1, creates, "an arbitrary lookup error triggers the same create attempt as the facade")
}

// Both implementations return a nil message for an already-taken ID, even
// when the existing message belongs to another chat. The caller decides what
// an ID collision means; the writer must not disclose the existing row.
func TestOnceDuplicateResultMatchesFacadeAcrossChats(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, chatID := range []string{"messagewrite-parity-owner", "messagewrite-parity-other"} {
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "test-user", ProjectID: "test-project", CreatedAt: now, UpdatedAt: now, LastActive: now}))
		_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
		require.NoError(t, err)
	}
	o := repo.ORM()
	svc, err := messagewrite.New(messagewrite.Deps{Tx: repo, Order: store.NewMessageOrderCounterExtStore(o), Messages: store.NewMessageExtStore(o), Windows: store.NewContextWindowExtStore(o), Threads: store.NewThreadExtStore(o), Workflows: store.NewWorkflowExtStore(o), Blocks: store.NewMessageContentBlockExtStore(o), Attachments: store.NewAttachmentExtStore(o), Updates: repo})
	require.NoError(t, err)
	role := int32(pb.MessageRole_MESSAGE_ROLE_USER)
	_, created, err := repo.SaveMessageToThreadOnce(ctx, "messagewrite-parity-owner", "messagewrite-parity-owner", role, "private", nil, nil, nil, "messagewrite-parity-id")
	require.NoError(t, err)
	require.True(t, created)
	for _, save := range []struct {
		name string
		fn   func(context.Context, string, string, int32, string, *string, []string, *int32, string) (*core.Message, bool, error)
	}{{"facade", repo.SaveMessageToThreadOnce}, {"service", svc.SaveMessageToThreadOnce}} {
		t.Run(save.name, func(t *testing.T) {
			message, created, err := save.fn(ctx, "messagewrite-parity-other", "messagewrite-parity-other", role, "different", nil, nil, nil, "messagewrite-parity-id")
			require.NoError(t, err)
			require.False(t, created)
			require.Nil(t, message)
		})
	}
}
