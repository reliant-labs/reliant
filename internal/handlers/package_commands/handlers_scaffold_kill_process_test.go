package package_commands_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"

	package_commands "github.com/reliant-labs/reliant/internal/handlers/package_commands"
)

// The machine routing itself is covered in package_commands_processes_test.go.
// A machine the caller does not own is refused before anything is sent: the
// harness's repository knows no machine.
func TestKillProcess_Generated(t *testing.T) {
	t.Parallel()

	svc := package_commands.NewTestPackageCommands(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.KillPackageProcessRequest, pb.KillPackageProcessResponse]{
		{
			Name:    "process_id is required",
			Req:     connect.NewRequest(&pb.KillPackageProcessRequest{}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a machine that is not the caller's is not found",
			Req:     connect.NewRequest(&pb.KillPackageProcessRequest{ProcessId: "p1", DaemonId: ptr.Of("someone-elses-machine")}),
			Ctx:     package_commands.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.KillProcess)
}
