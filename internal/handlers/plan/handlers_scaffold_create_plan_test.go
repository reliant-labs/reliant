package plan_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

// CreatePlan authorizes the caller through the chat before it validates the
// request, so a validation row names a chat the caller owns.
func TestCreatePlan_Generated(t *testing.T) {
	t.Parallel()

	var created *db.Plan
	repo := newPlanRepo()
	repo.CreatePlanFunc = func(_ context.Context, p *db.Plan) error {
		created = p
		return nil
	}
	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.CreatePlanRequest, pb.CreatePlanResponse]{
		{
			Name:    "unknown chat is not found",
			Req:     &pb.CreatePlanRequest{ChatId: missingID, Title: "Plan"},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is denied",
			Req:     &pb.CreatePlanRequest{ChatId: otherChatID, Title: "Plan"},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name:    "title is required",
			Req:     &pb.CreatePlanRequest{ChatId: ownChatID},
			Ctx:     plan.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "a new plan is pending and lives in the chat's thread",
			Req:  &pb.CreatePlanRequest{ChatId: ownChatID, Title: "Ship it"},
			Ctx:  plan.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.CreatePlanResponse) {
				require.NotNil(t, created)
				require.Equal(t, ownChatID, created.ThreadID)
				require.Equal(t, created.ID, resp.GetPlan().GetId())
				require.Equal(t, ownChatID, resp.GetPlan().GetChatId())
				require.Equal(t, "Ship it", resp.GetPlan().GetTitle())
				require.Equal(t, pb.PlanStatus_PLAN_STATUS_PENDING, resp.GetPlan().GetStatus())
			},
		},
	}, svc.CreatePlan)
}
