package run_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// The delegation to the chat service runs end to end in e2e/stories; these
// rows pin the ownership checks that run before it.
func TestInterruptRun_Generated(t *testing.T) {
	t.Parallel()

	// The generated harness stubs Repo, so no run exists.
	svc := run.NewTestRun(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.InterruptRunRequest, pb.InterruptRunResponse]{
		{
			Name:    "run_id is required",
			Req:     connect.NewRequest(&pb.InterruptRunRequest{}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown run is not found",
			Req:     connect.NewRequest(&pb.InterruptRunRequest{RunId: "run-1"}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.InterruptRun)
}
