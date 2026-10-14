package forge_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	forge "github.com/reliant-labs/reliant/internal/handlers/forge"
)

func TestDiffEnv_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedProjectService(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DiffForgeEnvRequest, pb.DiffForgeEnvResponse]{
		{
			Name:    "neither an env nor all is invalid",
			Req:     connect.NewRequest(&pb.DiffForgeEnvRequest{ProjectId: ownProjectID}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an env and all together are invalid",
			Req:     connect.NewRequest(&pb.DiffForgeEnvRequest{ProjectId: ownProjectID, Env: "dev", All: true}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's project is not found",
			Req:     connect.NewRequest(&pb.DiffForgeEnvRequest{ProjectId: otherProjectID, All: true}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.DiffEnv)
}
