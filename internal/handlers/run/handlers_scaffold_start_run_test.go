package run_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// Starting delegates to the chat service, which runs end to end in
// e2e/stories; these rows pin the checks that run before it.
func TestStartRun_Generated(t *testing.T) {
	t.Parallel()

	// The generated harness stubs Repo, so no session or project exists.
	svc := run.NewTestRun(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.StartRunRequest, pb.StartRunResponse]{
		{
			Name:    "workflow is required",
			Req:     connect.NewRequest(&pb.StartRunRequest{}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown session is not found",
			Req:     connect.NewRequest(&pb.StartRunRequest{Workflow: "builtin://agent", SessionId: "chat-1"}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "no session and no project to host one",
			Req:     connect.NewRequest(&pb.StartRunRequest{Workflow: "builtin://agent"}),
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.StartRun)
}
