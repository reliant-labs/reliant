package settings_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestUpdateShortcuts_Generated(t *testing.T) {
	t.Parallel()

	t.Run("the first write creates the shortcuts setting", func(t *testing.T) {
		stored := memorySettings{}
		svc := newSettingsService(t, stored.repo())
		resp, err := svc.UpdateShortcuts(settings.AuthedContext(t), connect.NewRequest(&pb.UpdateShortcutsRequest{Shortcuts: `{"newChat":"mod+n"}`}))
		require.NoError(t, err)
		require.True(t, resp.Msg.GetSuccess())
		require.Equal(t, `{"newChat":"mod+n"}`, stored["shortcuts"].Value)
	})

	t.Run("a failed read is internal", func(t *testing.T) {
		repo := memorySettings{}.repo()
		repo.GetSettingFunc = func(context.Context, string, *string, string) (*db.Setting, error) {
			return nil, errors.New("db down")
		}
		svc := newSettingsService(t, repo)
		_, err := svc.UpdateShortcuts(settings.AuthedContext(t), connect.NewRequest(&pb.UpdateShortcutsRequest{Shortcuts: "{}"}))
		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	})
}
