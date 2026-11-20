package settings_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestGetShortcuts_Generated(t *testing.T) {
	t.Parallel()

	t.Run("unset shortcuts answer empty, so the client uses its defaults", func(t *testing.T) {
		svc := newSettingsService(t, memorySettings{}.repo())
		resp, err := svc.GetShortcuts(settings.AuthedContext(t), &pb.GetShortcutsRequest{})
		require.NoError(t, err)
		require.Empty(t, resp.GetShortcuts())
	})

	t.Run("stored shortcuts", func(t *testing.T) {
		svc := newSettingsService(t, memorySettings{
			"shortcuts": {ID: "s-1", UserID: "test-user", Key: "shortcuts", Value: `{"newChat":"mod+n"}`},
		}.repo())
		resp, err := svc.GetShortcuts(settings.AuthedContext(t), &pb.GetShortcutsRequest{})
		require.NoError(t, err)
		require.Equal(t, `{"newChat":"mod+n"}`, resp.GetShortcuts())
	})
}
