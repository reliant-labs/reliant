package chat_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestListChatPlans_Generated(t *testing.T) {
	t.Parallel()

	repo := newChatRepo()
	repo.ListPlansByChatIDFunc = func(_ context.Context, chatID string) ([]*db.Plan, error) {
		require.Equal(t, ownChatID, chatID)
		now := time.Now()
		return []*db.Plan{{ID: "plan-1", Title: "Ship it", CreatedAt: now, UpdatedAt: now}}, nil
	}
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListChatPlansRequest, pb.ListChatPlansResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.ListChatPlansRequest{}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.ListChatPlansRequest{ChatId: missingChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.ListChatPlansRequest{ChatId: otherChatID}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's chat lists its plans",
			Req:  connect.NewRequest(&pb.ListChatPlansRequest{ChatId: ownChatID}),
			Ctx:  chat.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListChatPlansResponse]) {
				require.Len(t, resp.Msg.GetPlans(), 1)
				require.Equal(t, "plan-1", resp.Msg.GetPlans()[0].GetId())
				require.Equal(t, ownChatID, resp.Msg.GetPlans()[0].GetChatId())
				require.Equal(t, int32(1), resp.Msg.GetTotal())
			},
		},
	}, svc.ListChatPlans)
}
