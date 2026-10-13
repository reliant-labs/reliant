package package_commands_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"

	package_commands "github.com/reliant-labs/reliant/internal/handlers/package_commands"
)

// favoritesNoProjectRepo knows no project. The harness's stub would answer
// the ownership check with (nil, nil), which is not what a missing row looks
// like.
type favoritesNoProjectRepo struct{ package_commands.Repository }

func (favoritesNoProjectRepo) GetProjectWithUserCheck(context.Context, string, string) (*db.Project, error) {
	return nil, core.ErrProjectNotFound
}

func TestGetCommandFavorites_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Repo: favoritesNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetCommandFavoritesRequest, pb.GetCommandFavoritesResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.GetCommandFavoritesRequest{ProjectId: "p-1"}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.GetCommandFavoritesRequest{}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.GetCommandFavoritesRequest{ProjectId: "p-missing"}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetCommandFavorites)
}
