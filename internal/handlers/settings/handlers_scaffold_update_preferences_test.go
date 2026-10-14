package settings_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdatePreferences_Generated(t *testing.T) {
	t.Parallel()

	stored := memorySettings{}
	svc := newSettingsService(t, stored.repo())

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdatePreferencesRequest, pb.UpdatePreferencesResponse]{
		{
			Name:    "an unknown worktree_archive_mode is rejected",
			Req:     connect.NewRequest(&pb.UpdatePreferencesRequest{WorktreeArchiveMode: ptr.Of("sometimes")}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "only the preferences set are written",
			Req: connect.NewRequest(&pb.UpdatePreferencesRequest{
				StreamingEnabled:    ptr.Of(true),
				WorktreeArchiveMode: ptr.Of("always_keep"),
			}),
			Ctx: settings.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.UpdatePreferencesResponse]) {
				require.True(t, resp.Msg.GetSuccess())
				require.Equal(t, "true", stored["features.streaming_enabled"].Value)
				require.Equal(t, "always_keep", stored["worktree.archive_cleanup_mode"].Value)
				require.NotContains(t, stored, "worktree.default_delete_branch")
			},
		},
	}, svc.UpdatePreferences)
}
