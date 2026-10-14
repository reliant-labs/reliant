package settings_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestGetPrompts_Generated(t *testing.T) {
	t.Parallel()

	get := func(t *testing.T, stored memorySettings) (*connect.Response[pb.GetPromptsResponse], error) {
		t.Helper()
		return newSettingsService(t, stored.repo()).GetPrompts(settings.AuthedContext(t), connect.NewRequest(&pb.GetPromptsRequest{}))
	}

	t.Run("unset prompts answer an empty list", func(t *testing.T) {
		resp, err := get(t, memorySettings{})
		require.NoError(t, err)
		require.Empty(t, resp.Msg.GetPrompts())
	})

	t.Run("stored prompts", func(t *testing.T) {
		resp, err := get(t, memorySettings{
			"global.prompts": {Key: "global.prompts", Value: `[{"id":"p-1","title":"Review","content":"Review this","hotkey":"mod+r"}]`},
		})
		require.NoError(t, err)
		require.Len(t, resp.Msg.GetPrompts(), 1)
		require.Equal(t, "Review", resp.Msg.GetPrompts()[0].GetTitle())
		require.Equal(t, "mod+r", resp.Msg.GetPrompts()[0].GetHotkey())
	})

	t.Run("unparseable stored prompts are internal", func(t *testing.T) {
		_, err := get(t, memorySettings{"global.prompts": {Key: "global.prompts", Value: "not json"}})
		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	})
}
