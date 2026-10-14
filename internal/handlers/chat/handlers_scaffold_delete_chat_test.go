package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestDeleteChat_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteChatRequest, pb.DeleteChatResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.DeleteChatRequest{}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.DeleteChatRequest{ChatId: missingChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.DeleteChatRequest{ChatId: otherChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.DeleteChat)
}
