package chat_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
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
	repo.GetChatCompactedThroughFunc = func(context.Context, string) (int64, error) { return 0, nil }
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.GetChatUpdatesRequest, pb.GetChatUpdatesResponse]{
		{
			Name:    "chat_id is required",
			Req:     &pb.GetChatUpdatesRequest{},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     &pb.GetChatUpdatesRequest{ChatId: missingChatID},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     &pb.GetChatUpdatesRequest{ChatId: otherChatID},
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's chat reports its latest sequence",
			Req:  &pb.GetChatUpdatesRequest{ChatId: ownChatID, SinceSeq: 3},
			Ctx:  chat.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.GetChatUpdatesResponse) {
				require.Empty(t, resp.GetUpdates())
				require.Equal(t, int64(7), resp.GetLatestSequence())
			},
			CheckCallInfo: func(t *testing.T, info *connect.CallInfo) {
				require.Equal(t, "0", info.ResponseHeader().Get(chat.ChatCompactedThroughHeader), "a chat never compacted reports 0")
			},
		},
	}, svc.GetChatUpdates)
}
