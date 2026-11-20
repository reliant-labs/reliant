package forge_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	forge "github.com/reliant-labs/reliant/internal/handlers/forge"
)

func TestStartDeployPlan_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedProjectService(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.StartForgeDeployPlanRequest, pb.StartForgeDeployResponse]{
		{
			Name:    "a missing env is invalid",
			Req:     &pb.StartForgeDeployPlanRequest{ProjectId: ownProjectID},
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's project is not found",
			Req:     &pb.StartForgeDeployPlanRequest{ProjectId: otherProjectID, Env: "staging"},
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.StartDeployPlan)
}
