package chat_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdateChat_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.UpdateChatRequest, pb.UpdateChatResponse]{
		{
			Name:    "chat_id is required",
			Req:     &pb.UpdateChatRequest{Title: ptr.Of("Renamed")},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     &pb.UpdateChatRequest{ChatId: missingChatID, Title: ptr.Of("Renamed")},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     &pb.UpdateChatRequest{ChatId: otherChatID, Title: ptr.Of("Renamed")},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.UpdateChat)
}
