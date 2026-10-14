package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestMarkUnreadChat_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.MarkUnreadChatRequest, pb.MarkUnreadChatResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.MarkUnreadChatRequest{}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.MarkUnreadChatRequest{ChatId: missingChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.MarkUnreadChatRequest{ChatId: otherChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "an archived chat cannot be marked unread",
			Req:     connect.NewRequest(&pb.MarkUnreadChatRequest{ChatId: archivedChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.MarkUnreadChat)
}
