package run_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// run_list_test.go covers listing against a real database; these rows need
// none.
func TestListRuns_Generated(t *testing.T) {
	t.Parallel()

	// No run or session exists (emptyRunRepo).
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: emptyRunRepo()}))
	parent, session, badToken := "run-1", "chat-1", "not-a-token!!"

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListRunsRequest, pb.ListRunsResponse]{
		{
			Name:    "unknown parent run is not found",
			Req:     connect.NewRequest(&pb.ListRunsRequest{ParentId: &parent}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "unknown session is not found",
			Req:     connect.NewRequest(&pb.ListRunsRequest{SessionId: &session}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "malformed page_token is rejected",
			Req:     connect.NewRequest(&pb.ListRunsRequest{PageToken: &badToken}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "UNSPECIFIED display state is rejected",
			Req: connect.NewRequest(&pb.ListRunsRequest{
				DisplayStates: []pb.RunDisplayState{pb.RunDisplayState_RUN_DISPLAY_STATE_UNSPECIFIED},
			}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.ListRuns)
}
