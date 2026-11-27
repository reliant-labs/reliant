// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestCheckClaims_Generated: rows for `tdd.RunRPCCases`. The scaffold row is SELF-DESTRUCTING: it matches only
// forge's stub sentinel (not a bare CodeUnimplemented), so it fails once you implement CheckClaims —
// replace it then with Check / WantErr rows. Depth: `forge skill load testing/patterns`.
package gtm_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

// TestCheckClaims_Generated runs the canonical scaffold rows for CheckClaims.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestCheckClaims_Generated(t *testing.T) {
	t.Parallel()

	// gtm.NewTestGtm (helpers_gen_test.go) returns the
	// gtm *Service wired with stubbed dependencies. Override one
	// with gtm.WithGtmDeps(...), or per-row via Case.Setup.
	svc := gtm.NewTestGtm(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CheckClaimsRequest, pb.CheckClaimsResponse]{
		{
			Name: "unimplemented_scaffold", // delete this row once you implement CheckClaims; it only asserts nobody has
			Req:  connect.NewRequest(&pb.CheckClaimsRequest{}),
			Ctx:  gtm.AuthedContext(t), // authenticated test claims (UserID "test-user", role "admin")
			// replace with Check: func(t *testing.T, resp *connect.Response[pb.CheckClaimsResponse]) {...}, or WantErr: connect.CodeInvalidArgument
			WantScaffoldStub: true,
		},
	}, svc.CheckClaims)
}
