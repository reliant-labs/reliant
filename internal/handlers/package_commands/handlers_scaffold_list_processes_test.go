package package_commands_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/internal/ptr"

	package_commands "github.com/reliant-labs/reliant/internal/handlers/package_commands"
)

// Processes is the real process manager: the harness's stub would answer
// with nothing either way.
func TestListProcesses_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Processes: pkgmgr.NewService()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListPackageProcessesRequest, pb.ListPackageProcessesResponse]{
		{
			Name: "a worktree with no processes lists none",
			Req:  connect.NewRequest(&pb.ListPackageProcessesRequest{WorktreeId: ptr.Of("wt-without-processes")}),
			Ctx:  package_commands.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListPackageProcessesResponse]) {
				assert.Empty(t, resp.Msg.Processes)
			},
		},
	}, svc.ListProcesses)
}
