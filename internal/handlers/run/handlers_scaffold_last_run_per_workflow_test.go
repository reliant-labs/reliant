package run_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// TestLastRunPerWorkflowRPC (run_list_test.go) covers the query against a real
// database; this row needs none.
func TestLastRunPerWorkflow_Generated(t *testing.T) {
	t.Parallel()

	// The generated harness stubs Repo, so the caller has no runs.
	svc := run.NewTestRun(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.LastRunPerWorkflowRequest, pb.LastRunPerWorkflowResponse]{
		{
			Name: "no runs answers an empty list",
			Req:  connect.NewRequest(&pb.LastRunPerWorkflowRequest{}),
			Ctx:  run.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.LastRunPerWorkflowResponse]) {
				if got := len(resp.Msg.GetRuns()); got != 0 {
					t.Fatalf("runs = %d, want 0", got)
				}
			},
		},
	}, svc.LastRunPerWorkflow)
}
