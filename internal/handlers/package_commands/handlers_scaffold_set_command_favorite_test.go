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

// setFavoriteNoProjectRepo knows no project (see favoritesNoProjectRepo).
type setFavoriteNoProjectRepo struct{ package_commands.Repository }

func (setFavoriteNoProjectRepo) GetProjectWithUserCheck(context.Context, string, string) (*db.Project, error) {
	return nil, core.ErrProjectNotFound
}

func TestSetCommandFavorite_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Repo: setFavoriteNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SetCommandFavoriteRequest, pb.SetCommandFavoriteResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.SetCommandFavoriteRequest{ProjectId: "p-1", CommandKey: "npm:build"}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.SetCommandFavoriteRequest{CommandKey: "npm:build"}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "command_key is required",
			Req:     connect.NewRequest(&pb.SetCommandFavoriteRequest{ProjectId: "p-1"}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.SetCommandFavoriteRequest{ProjectId: "p-missing", CommandKey: "npm:build", IsFavorite: true}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.SetCommandFavorite)
}
