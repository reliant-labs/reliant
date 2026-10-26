package chat_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestSearchChats_Generated(t *testing.T) {
	t.Parallel()

	// The stubbed Repo fails closed: an empty query must answer without it.
	svc := chat.NewTestChat(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SearchChatsRequest, pb.SearchChatsResponse]{
		{
			Name: "an empty query matches nothing across all projects",
			Req:  connect.NewRequest(&pb.SearchChatsRequest{}),
			Ctx:  chat.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.SearchChatsResponse]) {
				require.Empty(t, resp.Msg.GetChats())
			},
		},
		{
			Name: "an empty query matches nothing",
			Req:  connect.NewRequest(&pb.SearchChatsRequest{ProjectId: "project-1"}),
			Ctx:  chat.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.SearchChatsResponse]) {
				require.Empty(t, resp.Msg.GetChats())
				require.Zero(t, resp.Msg.GetTotal())
			},
		},
	}, svc.SearchChats)
}
