package provider_account

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetChatProviderAccount returns the account the chat's root thread is using now, for the
// provider its latest call went to (the newest sticky assignment). The account is unset while the
// root thread has none yet, which is every chat before its first model call. A chat that is
// another user's, or absent, is NotFound.
func (s *Service) GetChatProviderAccount(
	ctx context.Context,
	req *connect.Request[pb.GetChatProviderAccountRequest],
) (*connect.Response[pb.GetChatProviderAccountResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("chat_id is required"))
	}
	if _, err := s.deps.Store.GetChatWithUserCheck(ctx, chatID, userID); err != nil {
		return nil, notFoundOr(err, "chat")
	}
	root, err := s.deps.Store.GetRootThread(ctx, chatID)
	if err != nil || root == nil {
		// A chat with no thread has made no model call.
		return connect.NewResponse(&pb.GetChatProviderAccountResponse{}), nil
	}
	assignments, err := s.deps.Store.ListThreadAssignments(ctx, root.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if len(assignments) == 0 {
		return connect.NewResponse(&pb.GetChatProviderAccountResponse{}), nil
	}
	accounts, err := s.deps.Store.ListProviderAccounts(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	limits, err := s.deps.Store.ListAccountLimits(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	now := time.Now()
	for _, a := range assignments {
		for _, acct := range accounts {
			if acct.ID == a.AccountID {
				return connect.NewResponse(&pb.GetChatProviderAccountResponse{Account: proto(acct, limits, now)}), nil
			}
		}
	}
	return connect.NewResponse(&pb.GetChatProviderAccountResponse{}), nil
}
