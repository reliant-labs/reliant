package question_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	question "github.com/reliant-labs/reliant/internal/handlers/question"
)

// TestListPendingQuestionsRejectsUnknownChat (question_pending_lookup_test.go)
// covers the lookup against a real database; these rows need none.
func TestListPendingQuestions_Generated(t *testing.T) {
	t.Parallel()

	// The generated harness stubs Repo, so no chat exists.
	svc := question.NewTestQuestion(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListPendingQuestionsRequest, pb.ListPendingQuestionsResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.ListPendingQuestionsRequest{}),
			Ctx:     question.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.ListPendingQuestionsRequest{ChatId: "chat-1"}),
			Ctx:     question.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListPendingQuestions)
}
