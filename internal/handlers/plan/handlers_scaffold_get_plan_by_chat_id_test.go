package plan_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

func TestGetPlanByChatId_Generated(t *testing.T) {
	t.Parallel()

	repo := newPlanRepo()
	repo.ListPlansByChatIDFunc = func(context.Context, string) ([]*db.Plan, error) { return nil, nil }
	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetPlanByChatIdRequest, pb.GetPlanByChatIdResponse]{
		{
			Name:    "unknown chat is not found",
			Req:     connect.NewRequest(&pb.GetPlanByChatIdRequest{ChatId: missingID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is denied",
			Req:     connect.NewRequest(&pb.GetPlanByChatIdRequest{ChatId: otherChatID}),
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "a chat with no plan answers an empty response",
			Req:  connect.NewRequest(&pb.GetPlanByChatIdRequest{ChatId: ownChatID}),
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetPlanByChatIdResponse]) {
				require.Nil(t, resp.Msg.GetPlan())
			},
		},
	}, svc.GetPlanByChatId)
}
