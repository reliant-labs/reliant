// forge:deps — consumer-declared interfaces for this package's Deps.
package chatprlinks

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Store keeps the links. store.ChatPrLinkExtStore satisfies it.
type Store interface {
	WriteChatPRLinks(ctx context.Context, w core.ChatPRLinkWrite) (int, error)
	ListChatPRLinks(ctx context.Context, chatID string) ([]core.ChatPRLink, error)
	ListWorktreeChatIDs(ctx context.Context, userID, worktreeID string) ([]string, error)
}

// Chats reads the chat a link read is decided on, with no owner check.
// *db.Repo satisfies it.
type Chats interface {
	GetChat(ctx context.Context, id string) (*core.Chat, error)
}
