// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestListMachineDirectory_Generated: the request-shape rows for `tdd.RunRPCCases`; the machine-scope
// behaviour (owner only, the credential guard, old daemons) is fs_proxy_machine_test.go's.
package file_system_test

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
	"github.com/reliant-labs/reliant/pkg/config"
)

// TestListMachineDirectory_Generated runs the canonical scaffold rows for ListMachineDirectory.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestListMachineDirectory_Generated(t *testing.T) {
	t.Parallel()

	// file_system.NewTestFileSystem (helpers_gen_test.go) returns the
	// file_system *Service wired with stubbed dependencies. Override one
	// with file_system.WithFileSystemDeps(...), or per-row via Case.Setup.
	// Every required dep is named (internal/architecture's
	// TestEveryConstructionSetsEveryRequiredDepsField); all but Placement are
	// the harness's own defaults, which these rows never reach.
	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{
		Logger:    testkit.DiscardLogger(),
		Config:    config.Defaults(),
		Router:    &file_system.MockRouter{},
		Repo:      &file_system.MockRepository{},
		Placement: previewNoProjectRepo{},
	}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListMachineDirectoryRequest, pb.ListMachineDirectoryResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.ListMachineDirectoryRequest{ProjectId: "p-1", Path: proto.String("/tmp")}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.ListMachineDirectoryRequest{Path: proto.String("/tmp")}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.ListMachineDirectoryRequest{ProjectId: "p-missing", Path: proto.String("/tmp")}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListMachineDirectory)
}
