// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package message

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Repository is the slice of *db.Repo the message handlers use.
type Repository interface {
	GetMessage(ctx context.Context, id string) (*db.Message, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*db.Attachment, error)
	ListToolCallsByMessageIDs(ctx context.Context, messageIDs []string) ([]*db.ToolCall, error)
}

// Chats reads the chat that owns a message. store.ChatExtStore satisfies it.
type Chats interface {
	GetChatRow(ctx context.Context, id string) (*core.ChatRow, error)
}

// Senders titles the senders of the messages a read assembles (a sender that
// is another run's main thread is titled by that run).
// store.ThreadExtStore satisfies it.
type Senders interface {
	SenderTitlesByThreadIDs(ctx context.Context, threadIDs []string) (map[string]string, error)
}
