package system_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/version"
)

func TestVersion_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)
	build := version.Get()

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.VersionRequest, pb.VersionResponse]{
		{
			Name: "build info",
			Req:  &pb.VersionRequest{},
			Check: func(t *testing.T, resp *pb.VersionResponse) {
				got := resp
				if got.Version != build.Version || got.Commit != build.Commit || got.Date != build.Date || got.Branch != build.Branch {
					t.Fatalf("Version = %+v, want %+v", got, build)
				}
			},
		},
	}, svc.Version)
}
