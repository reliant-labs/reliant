package chat

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// ListBranches lists all chats branched from a parent chat
// Branches are identified via Thread records - a chat is a branch if its root thread
// has a parent thread in the requested chat
func (s *Service) ListBranches(
	ctx context.Context,
	req *connect.Request[reliantv1.ListBranchesRequest],
) (*connect.Response[reliantv1.ListBranchesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Verify user owns the parent chat
	chat, err := s.chatRows().GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanReadChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// List all chats for the user
	allChats, err := s.chatLists().ListChats(ctx, db.ChatFilters{
		UserID: userID,
		Limit:  1000,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list branches"))
	}

	// Find branches by checking each chat's root thread for fork metadata
	// A chat is a branch if its root thread's parent is in the requested chat
	var branches []*reliantv1.BranchInfo
	for _, chat := range allChats {
		if chat.WorkflowID == nil || *chat.WorkflowID == "" {
			continue
		}

		// Get the root thread to check fork metadata
		threadRecord, parentChatID, err := s.deps.Repo.GetThreadWithParent(ctx, *chat.WorkflowID)
		if err != nil {
			// Skip chats without valid threads
			continue
		}

		// A branch is a FORK that crossed into another chat. Origin == fork
		// rules out spawn/node children and roots but doesn't say which chat
		// it came from; the parent chat_id comparison does that part, and
		// also establishes the crossing (this chat differs from the parent's,
		// or it would not be listed as a branch OF that chat).
		if threadRecord.Origin == db.ThreadOriginFork && parentChatID != nil && *parentChatID == req.Msg.ChatId {
			branch := &reliantv1.BranchInfo{
				Id:         chat.ID,
				Title:      chat.Title,
				CreatedAt:  chat.CreatedAt.Format(time.RFC3339),
				LastActive: chat.LastActive.Format(time.RFC3339),
			}
			if threadRecord.ForkAtMessageID != nil {
				if forkMsg, err := s.deps.Repo.GetMessage(ctx, *threadRecord.ForkAtMessageID); err == nil && forkMsg != nil {
					branch.BranchedAtOrdinal = &forkMsg.Ordinal
				}
			}
			branches = append(branches, branch)
		}
	}

	return connect.NewResponse(&reliantv1.ListBranchesResponse{
		Branches: branches,
		Total:    int32(len(branches)),
	}), nil
}
