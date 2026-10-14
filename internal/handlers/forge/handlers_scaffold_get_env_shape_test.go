package forge_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	forge "github.com/reliant-labs/reliant/internal/handlers/forge"
)

func TestGetEnvShape_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedProjectService(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetForgeEnvShapeRequest, pb.GetForgeEnvShapeResponse]{
		{
			Name:    "an anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.GetForgeEnvShapeRequest{ProjectId: ownProjectID, Env: "dev"}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "a missing env is invalid",
			Req:     connect.NewRequest(&pb.GetForgeEnvShapeRequest{ProjectId: ownProjectID}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a missing project_id is invalid",
			Req:     connect.NewRequest(&pb.GetForgeEnvShapeRequest{Env: "dev"}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's project is not found",
			Req:     connect.NewRequest(&pb.GetForgeEnvShapeRequest{ProjectId: otherProjectID, Env: "dev"}),
			Ctx:     forge.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetEnvShape)
}
