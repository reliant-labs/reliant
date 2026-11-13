package provider_account

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// GetChatProviderAccount returns the account the chat's root thread is using now: the one that
// served its newest assistant message, which follows the chat back to its own provider after a
// cross-provider fallback. A thread with no such message, or whose account is gone, falls back to
// the newest sticky assignment. The account is unset while the root thread has neither, which is
// every chat before its first model call. A chat that is another user's, or absent, is NotFound.
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
	chat, err := s.deps.Store.GetChatRowWithUserCheck(ctx, chatID, userID)
	if err != nil {
		return nil, notFoundOr(err, "chat")
	}
	root, err := s.deps.Store.GetRootThread(ctx, chatID)
	if err != nil || root == nil {
		// A chat with no thread has made no model call.
		return connect.NewResponse(&pb.GetChatProviderAccountResponse{}), nil
	}
	servingID, err := s.deps.Store.LatestServingAccount(ctx, root.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	assignments, err := s.deps.Store.ListThreadAssignments(ctx, root.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if servingID == "" && len(assignments) == 0 {
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
	render := func(acct core.ProviderAccount) *pb.ProviderAccount { return proto(acct, limits, now) }
	// A chat of an org may run on one of the org's own accounts: it is looked up there too, and
	// shown as a member sees it (never its key hint).
	if chat.OrgID != nil && *chat.OrgID != "" {
		owner := core.OrgAccountOwner(*chat.OrgID)
		orgAccounts, err := s.deps.Store.ListOwnedProviderAccounts(ctx, owner)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		if len(orgAccounts) > 0 {
			orgLimits, err := s.deps.Store.ListOwnedAccountLimits(ctx, owner)
			if err != nil {
				return nil, svcerr.Wrap(err)
			}
			personal := make(map[string]bool, len(accounts))
			for _, a := range accounts {
				personal[a.ID] = true
			}
			accounts = append(accounts, orgAccounts...)
			render = func(acct core.ProviderAccount) *pb.ProviderAccount {
				if personal[acct.ID] {
					return proto(acct, limits, now)
				}
				return orgAccountForMember(proto(acct, orgLimits, now), acct)
			}
		}
	}
	for _, acct := range accounts {
		if servingID != "" && acct.ID == servingID {
			return connect.NewResponse(&pb.GetChatProviderAccountResponse{Account: render(acct)}), nil
		}
	}
	for _, a := range assignments {
		for _, acct := range accounts {
			if acct.ID == a.AccountID {
				return connect.NewResponse(&pb.GetChatProviderAccountResponse{Account: render(acct)}), nil
			}
		}
	}
	return connect.NewResponse(&pb.GetChatProviderAccountResponse{}), nil
}

// orgAccountForMember narrows an org's account to what any member of the org may see of it
// (OrgProviderAccountService.ListOrgProviderAccounts): its provider, label, status and who pays,
// never its key hint, usage windows or a needs-attention reason.
func orgAccountForMember(full *pb.ProviderAccount, a core.ProviderAccount) *pb.ProviderAccount {
	label := a.Label
	if label == "" {
		label = full.GetProviderDisplayName() + " key"
	}
	return &pb.ProviderAccount{
		Id: full.Id, Provider: full.Provider, ProviderDisplayName: full.ProviderDisplayName,
		AuthKind: full.AuthKind, Label: label, Priority: full.Priority, Status: full.Status,
		LimitedUntil: full.LimitedUntil, CreatedAt: full.CreatedAt,
		Funding: full.Funding, FundingOrgId: full.FundingOrgId,
	}
}
