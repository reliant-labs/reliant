package system_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	system "github.com/reliant-labs/reliant/internal/handlers/system"
	"github.com/reliant-labs/reliant/internal/version"
)

func TestInfo_Generated(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	build := version.Get()

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.InfoRequest, pb.InfoResponse]{
		{
			Name: "build info and worktree",
			Req:  &pb.InfoRequest{},
			Check: func(t *testing.T, resp *pb.InfoResponse) {
				got := resp
				if got.Version != build.Version || got.Commit != build.Commit || got.Date != build.Date || got.Branch != build.Branch {
					t.Fatalf("Info build = %+v, want %+v", got, build)
				}
				if got.Api != "v2" {
					t.Fatalf("Info api = %q, want v2", got.Api)
				}
				if got.WorktreeName != filepath.Base(wd) {
					t.Fatalf("Info worktree = %q, want %q", got.WorktreeName, filepath.Base(wd))
				}
			},
		},
	}, svc.Info)
}
