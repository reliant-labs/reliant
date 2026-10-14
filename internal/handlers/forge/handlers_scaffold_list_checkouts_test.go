package forge_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	forge "github.com/reliant-labs/reliant/internal/handlers/forge"
)

func TestListCheckouts_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedProjectService(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListForgeCheckoutsRequest, pb.ListForgeCheckoutsResponse]{
		{
			Name:    "a missing project_id is invalid",
			Req:     connect.NewRequest(&pb.ListForgeCheckoutsRequest{}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's project is not found",
			Req:     connect.NewRequest(&pb.ListForgeCheckoutsRequest{ProjectId: otherProjectID}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListCheckouts)
}
