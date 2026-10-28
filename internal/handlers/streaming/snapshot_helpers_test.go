// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// buildChatSnapshotByID reads the chat the way StreamChatUpdates does, then
// builds its snapshot from that row.
func (s *Service) buildChatSnapshotByID(ctx context.Context, chatID string) (*reliantv1.ChatSyncSnapshot, int64, error) {
	chat, err := s.deps.Repo.GetChat(ctx, chatID)
	if err != nil {
		return nil, 0, err
	}
	if chat == nil {
		return nil, 0, fmt.Errorf("chat %s not found", chatID)
	}
	return s.buildChatSnapshot(ctx, chat)
}
