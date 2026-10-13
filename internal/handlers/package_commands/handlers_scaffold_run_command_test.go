package package_commands_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"

	package_commands "github.com/reliant-labs/reliant/internal/handlers/package_commands"
)

// runNoWorktreeRepo knows no worktree.
type runNoWorktreeRepo struct{ package_commands.Repository }

func (runNoWorktreeRepo) GetWorktree(context.Context, string) (*db.Worktree, error) {
	return nil, core.ErrWorktreeNotFound
}

// No row reaches Processes: every one is refused first, so nothing runs.
func TestRunCommand_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Repo: runNoWorktreeRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RunPackageCommandRequest, pb.RunPackageCommandResponse]{
		{
			Name:    "command_name is required",
			Req:     connect.NewRequest(&pb.RunPackageCommandRequest{PackageType: pb.PackageType_PACKAGE_TYPE_NPM, Path: ptr.Of("/w")}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "package_type is required",
			Req:     connect.NewRequest(&pb.RunPackageCommandRequest{CommandName: "build", Path: ptr.Of("/w")}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a worktree or a path is required",
			Req:     connect.NewRequest(&pb.RunPackageCommandRequest{CommandName: "build", PackageType: pb.PackageType_PACKAGE_TYPE_NPM}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown worktree is not found",
			Req:     connect.NewRequest(&pb.RunPackageCommandRequest{CommandName: "build", PackageType: pb.PackageType_PACKAGE_TYPE_NPM, WorktreeId: ptr.Of("wt-missing")}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.RunCommand)
}
