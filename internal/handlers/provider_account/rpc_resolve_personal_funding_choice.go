package provider_account

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// ResolvePersonalFundingChoice answers the chat's question before it moves from its org's
// accounts to the person's own (control-plane docs/design/org-provider-accounts.md §10.2).
//
// Person-only, and the chat's owner only: this is the one path by which a chat in an org spends
// the person's own account in place of the org's, so no agent, script or other member can take
// it. USE_PERSONAL records a funding lease that every waiting thread of the chat, sub-agents
// included, reads at its next model call; KEEP_WAITING keeps them waiting. remember also writes
// the person's preference for the org (provider.org_funding.on_org_limit: use_personal or wait).
// The chat's blocked threads are woken so they act on the answer at once.
func (s *Service) ResolvePersonalFundingChoice(
	ctx context.Context,
	req *connect.Request[pb.ResolvePersonalFundingChoiceRequest],
) (*connect.Response[pb.ResolvePersonalFundingChoiceResponse], error) {
	if err := orgs.RequirePerson(ctx, "decide whether a chat may use your own AI account"); err != nil {
		return nil, svcerr.Wrap(err)
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("chat_id is required"))
	}
	var state string
	var onLimit orgfunding.OnOrgLimit
	switch req.Msg.GetChoice() {
	case pb.ResolvePersonalFundingChoiceRequest_CHOICE_USE_PERSONAL:
		state, onLimit = core.PersonalFundingUsePersonal, orgfunding.OnOrgLimitUsePersonal
	case pb.ResolvePersonalFundingChoiceRequest_CHOICE_KEEP_WAITING:
		state, onLimit = core.PersonalFundingKeepWaiting, orgfunding.OnOrgLimitWait
	default:
		return nil, svcerr.Wrap(svcerr.InvalidArgument("choice must be USE_PERSONAL or KEEP_WAITING"))
	}
	if _, err := s.deps.Store.GetChatRowWithUserCheck(ctx, chatID, userID); err != nil {
		return nil, notFoundOr(err, "chat")
	}
	q, err := s.deps.Store.AnswerPersonalFunding(ctx, chatID, userID, state, time.Now())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if req.Msg.GetRemember() {
		current, _ := s.deps.Store.GetSettingValue(ctx, userID, orgfunding.SettingKey)
		next := orgfunding.Parse(current).With(q.OrgID, orgfunding.Preference{OnOrgLimit: onLimit}, nil)
		if err := s.deps.Store.SetString(ctx, userID, nil, orgfunding.SettingKey, next.Value()); err != nil {
			// The answer stands; only "don't ask again" did not stick.
			s.deps.Logger.WarnContext(ctx, "Could not remember the person's choice for the org", "chatID", chatID, "orgID", q.OrgID, "error", err)
		}
	}
	s.wakeChats(ctx, userID, "personal funding answered")
	return connect.NewResponse(&pb.ResolvePersonalFundingChoiceResponse{Question: personalFundingProto(q)}), nil
}
