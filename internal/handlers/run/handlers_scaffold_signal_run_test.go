package run_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// TestSignalRunOnPendingRunIsNotDelivered (internal/grpc/services, which
// builds the chat service) covers delivery against a real database; these
// rows pin the ownership checks that run before it.
func TestSignalRun_Generated(t *testing.T) {
	t.Parallel()

	// The generated harness stubs Repo, so no run exists.
	svc := run.NewTestRun(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SignalRunRequest, pb.SignalRunResponse]{
		{
			Name:    "run_id is required",
			Req:     connect.NewRequest(&pb.SignalRunRequest{}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown run is not found",
			Req:     connect.NewRequest(&pb.SignalRunRequest{RunId: "run-1"}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.SignalRun)
}
