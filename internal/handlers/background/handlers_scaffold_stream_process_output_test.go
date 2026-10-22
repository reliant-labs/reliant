package background_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	background "github.com/reliant-labs/reliant/internal/handlers/background"
)

// StreamProcessOutput checks its caller and request before it subscribes to
// the daemon's output.
func TestStreamProcessOutput_Generated(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts []background.TestOption
		req  *pb.StreamProcessOutputRequest
		want connect.Code
	}{
		{
			name: "a caller is required",
			opts: []background.TestOption{background.WithoutPrincipal()},
			req:  &pb.StreamProcessOutputRequest{ProcessId: "proc-1"},
			want: connect.CodeUnauthenticated,
		},
		{
			name: "process_id is required",
			req:  &pb.StreamProcessOutputRequest{},
			want: connect.CodeInvalidArgument,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, client := background.NewTestBackgroundServer(t, tc.opts...)
			stream, err := client.StreamProcessOutput(context.Background(), connect.NewRequest(tc.req))
			require.NoError(t, err)
			defer stream.Close()
			assert.False(t, stream.Receive(), "no event before the request is accepted")
			assert.Equal(t, tc.want, svcerr.Code(stream.Err()))
		})
	}
}
