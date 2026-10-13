package system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/version"
)

func TestVersion_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)
	build := version.Get()

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.VersionRequest, pb.VersionResponse]{
		{
			Name: "build info",
			Req:  connect.NewRequest(&pb.VersionRequest{}),
			Check: func(t *testing.T, resp *connect.Response[pb.VersionResponse]) {
				got := resp.Msg
				if got.Version != build.Version || got.Commit != build.Commit || got.Date != build.Date || got.Branch != build.Branch {
					t.Fatalf("Version = %+v, want %+v", got, build)
				}
			},
		},
	}, svc.Version)
}
