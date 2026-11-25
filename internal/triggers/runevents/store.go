// Copyright (c) 2025 Reliant Labs
package runevents

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// stores is the production Store: the generated run-event store for the
// outbox and the owner's trigger check, and the generated chat store for the
// chat a run event describes. Called with the ctx RunTx hands its fn, each
// call joins that transaction.
type stores struct {
	events store.RunEventExtStore
	chats  store.ChatsWithActivityExtStore
}

// NewStore is the Store over db.
func NewStore(db orm.Context) Store {
	return stores{events: store.NewRunEventExtStore(db), chats: store.NewChatsWithActivityExtStore(db)}
}

func (s stores) HasEnabledTriggerOfKind(ctx context.Context, userID string, kind core.TriggerKind) (bool, error) {
	return s.events.HasEnabledTriggerOfKind(ctx, userID, kind)
}

func (s stores) CreateRunEvent(ctx context.Context, ev *core.RunEvent) (bool, error) {
	return s.events.InsertRunEvent(ctx, ev)
}

func (s stores) LockChatForRunEvent(ctx context.Context, chatID string) error {
	return s.events.LockChatForRunEvent(ctx, chatID)
}

func (s stores) CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error) {
	return s.events.CountOtherPendingBlockers(ctx, chatID, excludeID)
}

func (s stores) GetChat(ctx context.Context, id string) (*core.Chat, error) {
	return s.chats.GetChat(ctx, id)
}
