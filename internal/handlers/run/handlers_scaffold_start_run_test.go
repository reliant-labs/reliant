package run_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// Starting delegates to the chat service, which runs end to end in
// e2e/stories; these rows pin the checks that run before it.
func TestStartRun_Generated(t *testing.T) {
	t.Parallel()

	// No session or project exists (emptyRunRepo).
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: emptyRunRepo()}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.StartRunRequest, pb.StartRunResponse]{
		{
			Name:    "workflow is required",
			Req:     &pb.StartRunRequest{},
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown session is not found",
			Req:     &pb.StartRunRequest{Workflow: "builtin://agent", SessionId: "chat-1"},
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "no session and no project to host one",
			Req:     &pb.StartRunRequest{Workflow: "builtin://agent"},
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.StartRun)
}
