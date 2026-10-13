package file_system_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
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

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListDirectoryRequest, pb.ListDirectoryResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.ListDirectoryRequest{}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "an empty path lists the directory the daemon resolved",
			Req:  connect.NewRequest(&pb.ListDirectoryRequest{}),
			Ctx:  file_system.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListDirectoryResponse]) {
				assert.Equal(t, "/home/u", resp.Msg.Path)
				require.Len(t, resp.Msg.Entries, 2)
				assert.Equal(t, "/home/u/src", resp.Msg.Entries[0].Path)
				assert.True(t, resp.Msg.Entries[0].IsDirectory)
				assert.Equal(t, "/home/u/.env", resp.Msg.Entries[1].Path)
				assert.True(t, resp.Msg.Entries[1].IsHidden)
			},
		},
	}, svc.ListDirectory)
}
