package run_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// The delegation to the chat service runs end to end in e2e/stories; these
// rows pin the ownership checks that run before it.
func TestResumeRun_Generated(t *testing.T) {
	t.Parallel()

	// No run, session or project exists (emptyRunRepo).
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: emptyRunRepo()}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ResumeRunRequest, pb.ResumeRunResponse]{
		{
			Name:    "run_id is required",
			Req:     &pb.ResumeRunRequest{},
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown run is not found",
			Req:     &pb.ResumeRunRequest{RunId: "run-1"},
			Ctx:     run.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ResumeRun)
}
