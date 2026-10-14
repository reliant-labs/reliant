package plan_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

// ListPlansByProject was never implemented. It answers exactly as the
// embedded Unimplemented handler always did, not with forge's scaffold stub.
func TestListPlansByProject_AnswersUnimplemented(t *testing.T) {
	t.Parallel()

	svc := plan.NewTestPlan(t)
	_, err := svc.ListPlansByProject(plan.AuthedContext(t), connect.NewRequest(&pb.ListPlansByProjectRequest{ProjectId: "project-1"}))
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	require.ErrorContains(t, err, "reliant.v1.PlanService.ListPlansByProject is not implemented")
}
