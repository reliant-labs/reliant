package question

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// ListPendingQuestions returns every pending question in a chat, oldest first,
// or, with chat_id unset, in every chat the caller owns (set is a filter,
// unset is none; an empty chat_id is rejected rather than read as unset),
// each carrying the thread that asked it. A question whose asking run
// completed or was cancelled is left out: nothing can answer it, and shown in
// its thread's composer it would replace the input the user needs to message
// that thread.
//
// All of them, not "the chat's question": each spawned agent is a thread that
// can park on its own ask_user, so one chat can be waiting on the user in
// several places at once. Returning only the newest hid every older thread's
// question, and the user's reply to that thread went to its mailbox — which a
// thread parked on a question never drains.
//
// A set chat is verified to exist and be the caller's BEFORE the question lookup,
// which is the difference between "there is no gate" and "that is not a chat".
// Without the check both collapse into an empty-but-successful response, and
// the CLI renders it as "No open questions." with exit 0 — a clean-looking
// answer that is simply false. Someone supervising a run reads that and walks
// away from a workflow that is parked on a question. Every other supervision
// RPC (GetWorkflowExecutions, GetChatUpdates) already verifies.
func (s *Service) ListPendingQuestions(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPendingQuestionsRequest],
) (*connect.Response[reliantv1.ListPendingQuestionsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == nil {
		questions, err := s.pendingQuestionsOfUser(ctx, userID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list pending questions"))
		}
		return connect.NewResponse(&reliantv1.ListPendingQuestionsResponse{Questions: questionsToProto(questions, authz.AccessOwner)}), nil
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return nil, svcerr.InvalidArgument("chat_id, when set, must not be empty")
	}
	// One chat's questions serve its viewers too, as read-only cards:
	// answering stays the owner's (ResolveQuestion). One decision shapes them.
	ctx = authz.WithViewerReads(ctx)
	chat, err := s.deps.Repo.GetChatRow(ctx, chatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	access, denied := authz.ReadChatAccess(ctx, userID, chat)
	if denied != nil {
		return nil, svcerr.NotFound("chat")
	}
	questions, err := s.deps.Repo.ListAnswerablePendingQuestionsByChat(ctx, chatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list pending questions"))
	}
	return connect.NewResponse(&reliantv1.ListPendingQuestionsResponse{Questions: questionsToProto(questions, access)}), nil
}

// pendingQuestionsOfUser is every answerable pending question of the caller.
// The store lists the caller's questions; one in a chat they may no longer
// read (an org they left, a credential's other org) is not listed, as GetChat
// would not show its chat. Owner reads only: no viewer is served here.
func (s *Service) pendingQuestionsOfUser(ctx context.Context, userID string) ([]*db.Question, error) {
	questions, err := s.deps.Repo.ListAnswerablePendingQuestionsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	readable := map[string]bool{}
	return authz.Visible(ctx, userID, questions, func(ctx context.Context, userID string, q *db.Question) bool {
		ok, seen := readable[q.ChatID]
		if !seen {
			chat, getErr := s.deps.Repo.GetChatRow(ctx, q.ChatID)
			ok = getErr == nil && authz.CanReadChat(ctx, userID, chat)
			readable[q.ChatID] = ok
		}
		return ok
	}), nil
}
