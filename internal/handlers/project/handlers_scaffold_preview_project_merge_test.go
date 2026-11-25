// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestPreviewProjectMerge_Generated: rows for `tdd.RunRPCCases`. The scaffold row is SELF-DESTRUCTING: it matches only
// forge's stub sentinel (not a bare CodeUnimplemented), so it fails once you implement PreviewProjectMerge —
// replace it then with Check / WantErr rows. Depth: `forge skill load testing/patterns`.
package project_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

// TestPreviewProjectMerge_Generated runs the canonical scaffold rows for PreviewProjectMerge.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestPreviewProjectMerge_Generated(t *testing.T) {
	t.Parallel()

	// project.NewTestProject (helpers_gen_test.go) returns the
	// project *Service wired with stubbed dependencies. Override one
	// with project.WithProjectDeps(...), or per-row via Case.Setup.
	svc := project.NewTestProject(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PreviewProjectMergeRequest, pb.PreviewProjectMergeResponse]{
		{
			Name: "unimplemented_scaffold", // delete this row once you implement PreviewProjectMerge; it only asserts nobody has
			Req:  connect.NewRequest(&pb.PreviewProjectMergeRequest{}),
			Ctx:  project.AuthedContext(t), // authenticated test claims (UserID "test-user", role "admin")
			// replace with Check: func(t *testing.T, resp *connect.Response[pb.PreviewProjectMergeResponse]) {...}, or WantErr: connect.CodeInvalidArgument
			WantScaffoldStub: true,
		},
	}, svc.PreviewProjectMerge)
}
