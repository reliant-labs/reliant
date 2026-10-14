package forge_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	forge "github.com/reliant-labs/reliant/internal/handlers/forge"
)

func TestStartDeployPlan_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedProjectService(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.StartForgeDeployPlanRequest, pb.StartForgeDeployResponse]{
		{
			Name:    "a missing env is invalid",
			Req:     connect.NewRequest(&pb.StartForgeDeployPlanRequest{ProjectId: ownProjectID}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's project is not found",
			Req:     connect.NewRequest(&pb.StartForgeDeployPlanRequest{ProjectId: otherProjectID, Env: "staging"}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.StartDeployPlan)
}
