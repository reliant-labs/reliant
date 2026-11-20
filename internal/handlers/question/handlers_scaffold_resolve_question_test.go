package question_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	question "github.com/reliant-labs/reliant/internal/handlers/question"
)

// The resolve path itself (ownership, resolution, the thread reply and the
// workflow signal) runs end to end in e2e/stories; these rows pin the
// request checks that run before any of it.
func TestResolveQuestion_Generated(t *testing.T) {
	t.Parallel()

	// No question or chat exists (emptyQuestionRepo).
	svc := question.NewTestQuestion(t, question.WithQuestionDeps(question.Deps{Repo: emptyQuestionRepo()}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ResolveQuestionRequest, pb.ResolveQuestionResponse]{
		{
			Name:    "question_id is required",
			Req:     &pb.ResolveQuestionRequest{Action: "reply"},
			Ctx:     question.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "action is required",
			Req:     &pb.ResolveQuestionRequest{QuestionId: "q-1"},
			Ctx:     question.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown question is not found",
			Req:     &pb.ResolveQuestionRequest{QuestionId: "q-1", Action: "reply"},
			Ctx:     question.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ResolveQuestion)
}
