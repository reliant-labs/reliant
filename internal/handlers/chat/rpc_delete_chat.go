package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// DeleteChat permanently deletes an archived chat; one that is not archived is refused.
func (s *Service) DeleteChat(
	ctx context.Context,
	req *reliantv1.DeleteChatRequest,
) (*reliantv1.DeleteChatResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.getChatForUser(ctx, req.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// Only an archived chat is deleted. Archiving is UpdateChatState; this rpc
	// never does it. It used to archive an active chat and hard-delete an
	// archived one, so the same request meant opposite things depending on
	// state the caller might not have seen — an "archive" sent to a chat
	// another device had just archived deleted it for good.
	if chat.State != db.ChatStateArchived {
		return nil, svcerr.FailedPrecondition("archive the chat before deleting it permanently")
	}

	// Cancel workflow if still running (belt-and-suspenders for hard delete)
	if workflowID := chat.MainThreadID(); workflowID != "" {
		_ = s.deps.Temporal.CancelWorkflow(ctx, workflowID, "")
	}

	if err := s.deps.Repo.DeleteChat(ctx, req.ChatId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to permanently delete chat"))
	}

	return &reliantv1.DeleteChatResponse{
		Success:            true,
		Message:            "Chat permanently deleted",
		PermanentlyDeleted: true,
	}, nil
}
