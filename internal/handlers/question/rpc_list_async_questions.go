package question

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

// ListAsyncQuestions returns every async question in a chat — asked with
// ask_user without blocking: true, so the thread carried on — answered or not, oldest
// first. The chat timeline renders each as a card on its tool call: an answer
// form while pending, the answer once resolved. ListPendingQuestions leaves
// them out, because nothing is parked on one: shown in the composer it would
// take over the input of a thread that is still working.
//
// As in ListPendingQuestions, the chat is verified to be the caller's before
// the lookup, so "no questions" never stands in for "not your chat".
func (s *Service) ListAsyncQuestions(
	ctx context.Context,
	req *connect.Request[pb.ListAsyncQuestionsRequest],
) (*connect.Response[pb.ListAsyncQuestionsResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	// Serves viewers of a shared chat, as read-only cards.
	ctx = authz.WithViewerReads(ctx)

	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	access, denied := authz.ReadChatAccess(ctx, userID, chat)
	if denied != nil {
		return nil, svcerr.NotFound("chat")
	}

	questions, err := s.deps.Repo.ListAsyncQuestionsByChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list async questions"))
	}
	return connect.NewResponse(&pb.ListAsyncQuestionsResponse{Questions: questionsToProto(questions, access)}), nil
}
