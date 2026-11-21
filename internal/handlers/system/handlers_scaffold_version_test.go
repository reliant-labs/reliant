package system_test

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/tdd"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
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
		{
			Name: "no policy configured serves empty floors, not enforced",
			Req:  connect.NewRequest(&pb.VersionRequest{}),
			Check: func(t *testing.T, resp *connect.Response[pb.VersionResponse]) {
				requireMinimums(t, resp.Msg.GetClientMinimums(), emptyMinimums())
			},
		},
	}, svc.Version)
}

func TestVersion_ServesConfiguredClientMinimums(t *testing.T) {
	t.Parallel()

	svc := system.NewTestSystem(t, system.WithSystemDeps(system.Deps{ClientVersions: testPolicy()}))
	resp, err := svc.Version(t.Context(), connect.NewRequest(&pb.VersionRequest{}))
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	requireMinimums(t, resp.Msg.GetClientMinimums(), testPolicyMinimums())
}

// testPolicy configures some kinds and leaves others unset, and sets the
// daemon's commit-time key, which is the gateway's alone and must not be
// served.
func testPolicy() *configv1.ClientVersionPolicy {
	return &configv1.ClientVersionPolicy{
		WebMinVersion:           "1.9.0",
		WebUpgradeUrl:           "https://app.reliantlabs.io",
		DesktopMinVersion:       "1.9.0",
		DesktopUpgradeUrl:       "https://reliantlabs.io/download",
		DaemonMinVersion:        "1.8.2-dev",
		DaemonUpgradeCommand:    "reliant daemon restart",
		DaemonMinCommitTimeUnix: 1_763_000_000,
		CliMinVersion:           "1.9.0",
		CliUpgradeCommand:       "brew upgrade --cask reliant",
		Enforce:                 true,
	}
}

func testPolicyMinimums() *pb.ClientMinimums {
	return &pb.ClientMinimums{
		Web:      &pb.ClientMinimum{MinVersion: "1.9.0", UpgradeUrl: "https://app.reliantlabs.io"},
		Desktop:  &pb.ClientMinimum{MinVersion: "1.9.0", UpgradeUrl: "https://reliantlabs.io/download"},
		Daemon:   &pb.ClientMinimum{MinVersion: "1.8.2-dev", UpgradeCommand: "reliant daemon restart"},
		Cli:      &pb.ClientMinimum{MinVersion: "1.9.0", UpgradeCommand: "brew upgrade --cask reliant"},
		SdkGo:    &pb.ClientMinimum{},
		SdkTs:    &pb.ClientMinimum{},
		Enforced: true,
	}
}

func emptyMinimums() *pb.ClientMinimums {
	return &pb.ClientMinimums{
		Web: &pb.ClientMinimum{}, Desktop: &pb.ClientMinimum{}, Daemon: &pb.ClientMinimum{},
		Cli: &pb.ClientMinimum{}, SdkGo: &pb.ClientMinimum{}, SdkTs: &pb.ClientMinimum{},
	}
}

func requireMinimums(t *testing.T, got, want *pb.ClientMinimums) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Fatalf("client_minimums = %v, want %v", got, want)
	}
}
