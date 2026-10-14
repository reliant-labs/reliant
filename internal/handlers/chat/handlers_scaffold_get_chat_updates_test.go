package chat_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestGetChatUpdates_Generated(t *testing.T) {
	t.Parallel()

	repo := newChatRepo()
	repo.GetUpdatesSinceFunc = func(_ context.Context, chatID string, sinceSeq int64, _ int) ([]db.ChatUpdate, error) {
		require.Equal(t, ownChatID, chatID)
		require.Equal(t, int64(3), sinceSeq)
		return nil, nil
	}
	repo.GetLatestUpdateSequenceFunc = func(_ context.Context, chatID string) (int64, error) {
		return 7, nil
	}
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetChatUpdatesRequest, pb.GetChatUpdatesResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.GetChatUpdatesRequest{}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: missingChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: otherChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's chat reports its latest sequence",
			Req:  connect.NewRequest(&pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: 3}),
			Ctx:  chat.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetChatUpdatesResponse]) {
				require.Empty(t, resp.Msg.GetUpdates())
				require.Equal(t, int64(7), resp.Msg.GetLatestSequence())
			},
		},
	}, svc.GetChatUpdates)
}
