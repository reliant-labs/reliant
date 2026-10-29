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

// listNoWorktreeRepo knows no worktree.
type listNoWorktreeRepo struct{ package_commands.Repository }

func (listNoWorktreeRepo) GetWorktree(context.Context, string) (*db.Worktree, error) {
	return nil, core.ErrWorktreeNotFound
}

// The machine routing itself is covered in package_commands_processes_test.go.
func TestListProcesses_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Repo: listNoWorktreeRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListPackageProcessesRequest, pb.ListPackageProcessesResponse]{
		{
			Name:    "an unknown worktree is not found",
			Req:     connect.NewRequest(&pb.ListPackageProcessesRequest{WorktreeId: ptr.Of("wt-missing")}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListProcesses)
}
