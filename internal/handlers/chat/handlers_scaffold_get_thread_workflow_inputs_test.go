package chat_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestGetThreadWorkflowInputs_Generated(t *testing.T) {
	t.Parallel()

	repo := newChatRepo()
	// The store answers a thread with no workflow with (nil, nil).
	repo.GetWorkflowByThreadFunc = func(context.Context, string, string) (*db.Workflow, error) {
		return nil, nil
	}
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetThreadWorkflowInputsRequest, pb.GetThreadWorkflowInputsResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.GetThreadWorkflowInputsRequest{ThreadId: "thread-1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "thread_id is required",
			Req:     connect.NewRequest(&pb.GetThreadWorkflowInputsRequest{ChatId: ownChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.GetThreadWorkflowInputsRequest{ChatId: otherChatID, ThreadId: "thread-1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "a thread with no workflow is not found",
			Req:     connect.NewRequest(&pb.GetThreadWorkflowInputsRequest{ChatId: ownChatID, ThreadId: "thread-1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetThreadWorkflowInputs)
}
