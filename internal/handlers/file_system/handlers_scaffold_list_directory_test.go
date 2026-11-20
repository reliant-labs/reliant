package file_system_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
)

// homeListingRouter answers fs.list_dir the way the daemon does for an empty
// path: it resolves the user's home and lists it.
type homeListingRouter struct{ toolexectest.Stub }

func (*homeListingRouter) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return []byte(`{"path":"/home/u","entries":[{"name":"src","is_dir":true},{"name":".env","size":3}]}`), nil
}

func TestListDirectory_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Router: &homeListingRouter{}}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ListDirectoryRequest, pb.ListDirectoryResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     &pb.ListDirectoryRequest{},
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "an empty path lists the directory the daemon resolved",
			Req:  &pb.ListDirectoryRequest{},
			Ctx:  file_system.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.ListDirectoryResponse) {
				assert.Equal(t, "/home/u", resp.Path)
				require.Len(t, resp.Entries, 2)
				assert.Equal(t, "/home/u/src", resp.Entries[0].Path)
				assert.True(t, resp.Entries[0].IsDirectory)
				assert.Equal(t, "/home/u/.env", resp.Entries[1].Path)
				assert.True(t, resp.Entries[1].IsHidden)
			},
		},
	}, svc.ListDirectory)
}
