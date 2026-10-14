package run_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// TestLastRunPerWorkflowRPC (run_list_test.go) covers the query against a real
// database; this row needs none.
func TestLastRunPerWorkflow_Generated(t *testing.T) {
	t.Parallel()

	// The caller has no runs. The harness's auto-stub refuses an
	// unprogrammed call, so the one query this rpc makes is programmed.
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: &run.StubRepo{
		LastRunPerWorkflowFunc: func(context.Context, db.RunListFilters) ([]*db.RunListItem, error) {
			return nil, nil
		},
	}}))

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
