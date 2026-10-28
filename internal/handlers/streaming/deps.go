// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the streaming handlers
// read; mock_gen.go mocks it. *db.Repo satisfies it, and the composition root
// hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// every callee they hand the repository to (threads for the snapshot's
// messages, messageproto for its tool calls), so a callee that needs more
// points the compiler here.
package streaming

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/threads"
)

// Repository is everything the streaming handlers read.
type Repository interface {
	// The chat a subscription is for, and the trigger that started it.
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error)

	// The chat and user update logs a stream replays and tails.
	GetLatestUpdateSequence(ctx context.Context, chatID string) (int64, error)
	GetUpdatesSince(ctx context.Context, chatID string, sinceSeq int64, limit int) ([]db.ChatUpdate, error)
	GetLatestNonMessageUpdatesPerEntity(ctx context.Context, chatID string) ([]db.ChatUpdate, error)
	GetLatestUserUpdateSequence(ctx context.Context, userID string) (int64, error)
	GetUserUpdatesSince(ctx context.Context, userID string, sinceSeq int64, limit int) ([]db.UserUpdate, error)

	// What a chat snapshot is assembled from.
	ListThreadsByConversation(ctx context.Context, chatID string) ([]*db.Thread, error)
	CountMessagesInChat(ctx context.Context, chatID string) (int, error)
	ListRecentTranscriptSiblingMessages(ctx context.Context, chatID, mainThreadID string, fromSeq int64, limit int) ([]*db.Message, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*db.MessageContentBlock, error)
	ListLiveToolCallsByChat(ctx context.Context, chatID string) ([]*db.ToolCall, error)
	SpawnToolCallIDsByChildThread(ctx context.Context, chatID string) (map[string]string, error)

	// GetAttachmentsByIDs and GetContextUsage are called directly too; the
	// roles below already declare them.
	threads.Repository
	messageproto.Reader
}
