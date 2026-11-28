package messagewrite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/messagewrite"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMessageWriteCommitRollbackAndIdempotency(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	chatID := "messagewrite-transaction-test"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "test-user", ProjectID: "test-project", CreatedAt: now, UpdatedAt: now, LastActive: now}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	var notifications int
	repo.SetUpdateNotifiers(nil, func(string, int64, db.ChatUpdate) { notifications++ })
	o := repo.ORM()
	svc, err := messagewrite.New(messagewrite.Deps{Tx: repo, Order: store.NewMessageOrderCounterExtStore(o), Messages: store.NewMessageExtStore(o), Windows: store.NewContextWindowExtStore(o), Threads: store.NewThreadExtStore(o), Workflows: store.NewWorkflowExtStore(o), Blocks: store.NewMessageContentBlockExtStore(o), Attachments: store.NewAttachmentExtStore(o), Updates: repo})
	require.NoError(t, err)
	role := int32(pb.MessageRole_MESSAGE_ROLE_USER)
	rollback := errors.New("abort outer transaction")
	err = repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		_, err := svc.SaveMessageToThread(txCtx, chatID, chatID, role, "rolled back", nil, nil, nil)
		require.NoError(t, err)
		require.Zero(t, notifications, "notification waits for outer commit")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.Zero(t, notifications)
	rows, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, repo.CreateAttachment(ctx, &db.Attachment{ID: "messagewrite-attachment", UserID: "test-user", Filename: "example.png", MimeType: "image/png", AttachmentType: "image", FilePath: "/example.png", CreatedAt: now, UpdatedAt: now}))
	first, err := svc.SaveMessageToThreadWithID(ctx, chatID, chatID, role, "one", nil, []string{"messagewrite-attachment"}, nil, "messagewrite-first")
	require.NoError(t, err)
	require.Equal(t, "messagewrite-first", first.ID)
	blocks, err := repo.ListContentBlocks(ctx, first.ID)
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	require.Equal(t, pb.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, blocks[0].BlockType)
	require.Equal(t, pb.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, blocks[1].BlockType)
	require.Equal(t, "messagewrite-attachment", *blocks[1].Content)
	require.Equal(t, 1, notifications)
	second, created, err := svc.SaveMessageToThreadOnce(ctx, chatID, chatID, role, "two", nil, nil, nil, "messagewrite-once")
	require.NoError(t, err)
	require.True(t, created)
	require.Greater(t, second.Seq, first.Seq)
	require.Equal(t, 2, notifications)
	_, created, err = svc.SaveMessageToThreadOnce(ctx, chatID, chatID, role, "ignored", nil, nil, nil, "messagewrite-once")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, 2, notifications)
}
