package messagewrite_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/messagewrite"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
)

// A failure after the update was appended rolls back that attempt's rows and
// callback. Repo.RunTx retries its additional 23505 classification; only the
// successful attempt becomes visible or notifies the hub.
func TestRetryDiscardsFailedAttemptNotification(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	chatID := "messagewrite-retry-test"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "test-user", ProjectID: "test-project", CreatedAt: now, UpdatedAt: now, LastActive: now}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	var notifications, attempts int
	repo.SetUpdateNotifiers(nil, func(string, int64, db.ChatUpdate) { notifications++ })
	updates := &messagewrite.MockUpdates{CreateChatUpdateFunc: func(ctx context.Context, chatID string, typ pb.ChatUpdateType, id, data string) error {
		attempts++
		if err := repo.CreateChatUpdate(ctx, chatID, typ, id, data); err != nil {
			return err
		}
		if attempts == 1 {
			return &pgconn.PgError{Code: "23505", Message: "injected retry"}
		}
		return nil
	}}
	o := repo.ORM()
	svc, err := messagewrite.New(messagewrite.Deps{Tx: repo, Order: store.NewMessageOrderCounterExtStore(o), Messages: store.NewMessageExtStore(o), Windows: store.NewContextWindowExtStore(o), Threads: store.NewThreadExtStore(o), Workflows: store.NewWorkflowExtStore(o), Blocks: store.NewMessageContentBlockExtStore(o), Attachments: store.NewAttachmentExtStore(o), Updates: updates})
	require.NoError(t, err)
	_, err = svc.SaveMessageToThread(ctx, chatID, chatID, int32(pb.MessageRole_MESSAGE_ROLE_USER), "retry", nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	require.Equal(t, 1, notifications)
	rows, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
