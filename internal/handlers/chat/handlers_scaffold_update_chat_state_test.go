package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestUpdateChatState_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateChatStateRequest, pb.UpdateChatStateResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.UpdateChatStateRequest{State: pb.ChatState_CHAT_STATE_ARCHIVED}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "only idle and archived are user transitions",
			Req:     connect.NewRequest(&pb.UpdateChatStateRequest{ChatId: ownChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.UpdateChatStateRequest{ChatId: missingChatID, State: pb.ChatState_CHAT_STATE_ARCHIVED}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.UpdateChatStateRequest{ChatId: otherChatID, State: pb.ChatState_CHAT_STATE_ARCHIVED}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.UpdateChatState)
}
