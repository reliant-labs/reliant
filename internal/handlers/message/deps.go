// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package message

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the message handlers use.
type Repository interface {
	GetMessage(ctx context.Context, id string) (*db.Message, error)
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*db.Attachment, error)
	ListToolCallsByMessageIDs(ctx context.Context, messageIDs []string) ([]*db.ToolCall, error)
	ListThreadsByIDs(ctx context.Context, ids []string) ([]*db.Thread, error)
}
