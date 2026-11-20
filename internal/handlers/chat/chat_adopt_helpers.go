package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

func (s *Service) setChatAdopted(ctx context.Context, chatID string, adopted bool) (*db.Chat, error) {
	userID := auth.MustGetUserID(ctx)
	if chatID == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	owned, err := s.deps.Repo.SetChatAdopted(ctx, chatID, userID, adopted)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat"))
	}
	if !owned {
		return nil, svcerr.NotFound("chat")
	}
	chat, err := s.getChatForUser(ctx, chatID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to reload chat"))
	}
	// Adopting is taking the run into the chat list, so an archived one comes
	// back out of the archive: an automation archives every run it finishes
	// successfully, and "Open as chat" on one of those must land somewhere.
	if adopted && chat.State == db.ChatStateArchived {
		if err := s.deps.Repo.UpdateChatState(ctx, chatID, db.ChatStateIdle, "adopted"); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat"))
		}
		if chat, err = s.getChatForUser(ctx, chatID, userID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to reload chat"))
		}
	}
	return chat, nil
}
