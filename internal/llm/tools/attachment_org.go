package tools

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// chatGetter reads a chat by id; every attachment-writing tool's store is one.
type chatGetter interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// chatOrgID is the org stamped on an attachment a tool makes in chatID
// (attachments.org_id): the chat's org, or nil when the chat is in none, there
// is no chat, or it cannot be read. The stamp names an org, not the chat. Nil is never a guess, so an org-scoped
// read (store.GetAttachmentForUserInOrg) cannot reach the attachment from
// another org.
func chatOrgID(ctx context.Context, chats chatGetter, chatID string) *string {
	if chatID == "" {
		return nil
	}
	chat, err := chats.GetChat(ctx, chatID)
	if err != nil {
		return nil
	}
	return orgOf(chat)
}

// orgOf is chat's org as an attachment records it: nil for none.
func orgOf(chat *db.Chat) *string {
	if chat == nil || chat.OrgID == nil || *chat.OrgID == "" {
		return nil
	}
	org := *chat.OrgID
	return &org
}
