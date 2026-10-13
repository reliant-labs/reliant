package package_commands_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"

	package_commands "github.com/reliant-labs/reliant/internal/handlers/package_commands"
)

// Processes is the real process manager, which knows no such process.
func TestKillProcess_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Processes: pkgmgr.NewService()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.KillPackageProcessRequest, pb.KillPackageProcessResponse]{
		{
			Name:    "process_id is required",
			Req:     connect.NewRequest(&pb.KillPackageProcessRequest{}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			// Internal, not NotFound: KillProcess maps every failure other
			// than "is not running" to Internal.
			Name:    "unknown process fails",
			Req:     connect.NewRequest(&pb.KillPackageProcessRequest{ProcessId: "no-such-process"}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInternal,
		},
	}, svc.KillProcess)
}
