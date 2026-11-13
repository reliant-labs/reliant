package provider_account

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// GetPersonalFundingQuestion returns the chat's question before it moves from its org's accounts
// to the person's own (control-plane docs/design/org-provider-accounts.md §10), unset when it has
// none. The chat's owner only: another user's chat, or none, is NotFound.
func (s *Service) GetPersonalFundingQuestion(
	ctx context.Context,
	req *connect.Request[pb.GetPersonalFundingQuestionRequest],
) (*connect.Response[pb.GetPersonalFundingQuestionResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("chat_id is required"))
	}
	if _, err := s.deps.Store.GetChatRowWithUserCheck(ctx, chatID, userID); err != nil {
		return nil, notFoundOr(err, "chat")
	}
	q, err := s.deps.Store.GetChatPersonalFunding(ctx, chatID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	// The row is the chat's, written for its owner, whom the check above is.
	if q == nil {
		return connect.NewResponse(&pb.GetPersonalFundingQuestionResponse{}), nil
	}
	return connect.NewResponse(&pb.GetPersonalFundingQuestionResponse{Question: personalFundingProto(q)}), nil
}

// personalFundingProto is the wire form of a chat's question.
func personalFundingProto(q *core.PersonalFunding) *pb.PersonalFundingQuestion {
	out := &pb.PersonalFundingQuestion{
		ChatId: q.ChatID, OrgId: q.OrgID, Model: q.Model,
		PersonalProvider: q.PersonalProvider, PersonalAccountLabel: q.PersonalAccountLabel,
		AskedAt: q.AskedAt.UTC().Format(time.RFC3339),
	}
	switch q.Reason {
	case core.PersonalFundingLimited:
		out.Reason = pb.PersonalFundingQuestion_REASON_LIMITED
	case core.PersonalFundingUnusable:
		out.Reason = pb.PersonalFundingQuestion_REASON_UNUSABLE
	case core.PersonalFundingUnserved:
		out.Reason = pb.PersonalFundingQuestion_REASON_UNSERVED
	}
	switch q.State {
	case core.PersonalFundingAsked:
		out.State = pb.PersonalFundingQuestion_STATE_ASKED
	case core.PersonalFundingUsePersonal:
		out.State = pb.PersonalFundingQuestion_STATE_USE_PERSONAL
	case core.PersonalFundingKeepWaiting:
		out.State = pb.PersonalFundingQuestion_STATE_KEEP_WAITING
	}
	if q.OrgResetAt != nil {
		at := q.OrgResetAt.UTC().Format(time.RFC3339)
		out.OrgResetAt = &at
	}
	return out
}
