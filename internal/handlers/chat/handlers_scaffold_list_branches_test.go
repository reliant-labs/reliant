package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestListBranches_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListBranchesRequest, pb.ListBranchesResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.ListBranchesRequest{}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.ListBranchesRequest{ChatId: missingChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.ListBranchesRequest{ChatId: otherChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListBranches)
}
