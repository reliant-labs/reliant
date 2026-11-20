package chat_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestDismissChat_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DismissChatRequest, pb.DismissChatResponse]{
		{
			Name:    "chat_id is required",
			Req:     &pb.DismissChatRequest{},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     &pb.DismissChatRequest{ChatId: missingChatID},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     &pb.DismissChatRequest{ChatId: otherChatID},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.DismissChat)
}
