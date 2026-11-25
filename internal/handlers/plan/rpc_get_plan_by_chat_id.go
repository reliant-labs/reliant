package plan

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

// GetPlanByChatId retrieves a plan by chat ID
func (s *Service) GetPlanByChatId(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPlanByChatIdRequest],
) (*connect.Response[reliantv1.GetPlanByChatIdResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat (authz.WithViewerReads).
	ctx = authz.WithViewerReads(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// The owner, or a viewer of a shared chat.
	chat, err := s.deps.Chats.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	if _, denied := authz.ReadChatAccess(ctx, userID, chat); denied != nil {
		return nil, svcerr.NotFound("chat")
	}

	// Get plans for this chat (via thread join)
	plans, err := s.deps.Plans.ListPlansByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}

	// If no plans found, return empty response
	if len(plans) == 0 {
		return connect.NewResponse(&reliantv1.GetPlanByChatIdResponse{}), nil
	}

	// Return the first plan
	return connect.NewResponse(&reliantv1.GetPlanByChatIdResponse{Plan: corePlanToProto(plans[0], req.Msg.ChatId)}), nil
}
