package settings_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// TrackPageVisited reports to the process-wide analytics client (a no-op one
// in tests) and touches no repository: the auto-stubbed Repo would fail the
// call if it did.
func TestTrackPageVisited_Generated(t *testing.T) {
	t.Parallel()

	svc := settings.NewTestSettings(t, settings.WithPlaceholderDeps())
	resp, err := svc.TrackPageVisited(settings.AuthedContext(t), connect.NewRequest(&pb.TrackPageVisitedRequest{
		PageName:     "settings",
		PreviousPage: ptr.Of("chat"),
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.GetSuccess())
}
