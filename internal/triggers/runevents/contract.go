// Copyright (c) 2025 Reliant Labs
package runevents

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Store is what the emitter reads and writes; every call honors the ambient
// RunTx transaction. NewStore is the production one over the generated run
// event and chat stores. The chat itself is read through chatviews.Of(store):
// the store's own GetChat when it has one (NewStore's does), else the
// generated chat store over its database handle.
type Store interface {
	HasEnabledTriggerOfKind(ctx context.Context, userID string, kind core.TriggerKind) (bool, error)
	CreateRunEvent(ctx context.Context, ev *core.RunEvent) (bool, error)
	LockChatForRunEvent(ctx context.Context, chatID string) error
	CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error)
}

// ChatStore is the production store's optional chat-read capability. Store
// implementations without it retain the chatviews fallback.
type ChatStore interface {
	Store
	GetChat(ctx context.Context, id string) (*core.Chat, error)
}
