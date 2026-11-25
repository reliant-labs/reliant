// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// hubPublisher is chatupdates.Publisher over the two update hubs: each
// committed user and chat update goes out to the web clients watching it, as
// RepoUpdateFeed does for what the repository itself writes.
type hubPublisher struct {
	users *UserUpdateHub
	chats *ChatUpdateHub
}

func (p hubPublisher) PublishChat(chatID string, seq int64, update core.ChatUpdate) {
	p.chats.Publish(context.Background(), streaming.UpdateEvent[core.ChatUpdate]{Key: chatID, SequenceNumber: seq, Payload: update})
}

func (p hubPublisher) PublishUser(update *core.UserUpdate) {
	p.users.Publish(context.Background(), streaming.UpdateEvent[core.UserUpdate]{Key: update.UserID, SequenceNumber: update.SequenceNumber, Payload: *update})
}
