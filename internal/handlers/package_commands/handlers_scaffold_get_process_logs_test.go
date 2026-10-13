package package_commands_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"

	package_commands "github.com/reliant-labs/reliant/internal/handlers/package_commands"
)

// Processes is the real process manager, which knows no such process. The
// harness's stub would answer (nil, nil): a process that is not there.
func TestGetProcessLogs_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Processes: pkgmgr.NewService()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetPackageProcessLogsRequest, pb.GetPackageProcessLogsResponse]{
		{
			Name:    "process_id is required",
			Req:     connect.NewRequest(&pb.GetPackageProcessLogsRequest{}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown process is not found",
			Req:     connect.NewRequest(&pb.GetPackageProcessLogsRequest{ProcessId: "no-such-process"}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetProcessLogs)
}
