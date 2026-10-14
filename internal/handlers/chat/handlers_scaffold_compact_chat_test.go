package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestCompactChat_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: newChatRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CompactChatRequest, pb.CompactChatResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.CompactChatRequest{ThreadId: "thread-1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "thread_id is required",
			Req:     connect.NewRequest(&pb.CompactChatRequest{ChatId: ownChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.CompactChatRequest{ChatId: missingChatID, ThreadId: "thread-1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.CompactChatRequest{ChatId: otherChatID, ThreadId: "thread-1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.CompactChat)
}
