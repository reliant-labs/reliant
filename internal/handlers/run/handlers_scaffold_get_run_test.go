package run_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// Ownership resolves through the run's chat; these rows pin the checks that
// run before any read.
func TestGetRun_Generated(t *testing.T) {
	t.Parallel()

	// The generated harness stubs Repo, so no run exists.
	svc := run.NewTestRun(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetRunRequest, pb.GetRunResponse]{
		{
			Name:    "run_id is required",
			Req:     connect.NewRequest(&pb.GetRunRequest{}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown run is not found",
			Req:     connect.NewRequest(&pb.GetRunRequest{RunId: "run-1"}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetRun)
}
