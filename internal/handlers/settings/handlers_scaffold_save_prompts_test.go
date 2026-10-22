package settings_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestSavePrompts_Generated(t *testing.T) {
	t.Parallel()

	t.Run("prompts round-trip through GetPrompts", func(t *testing.T) {
		stored := memorySettings{}
		svc := newSettingsService(t, stored.repo())
		ctx := settings.AuthedContext(t)

		resp, err := svc.SavePrompts(ctx, connect.NewRequest(&pb.SavePromptsRequest{Prompts: []*pb.UserPrompt{
			{Id: "p-1", Title: "Review", Content: "Review this", Category: ptr.Of("code")},
		}}))
		require.NoError(t, err)
		require.True(t, resp.Msg.GetSuccess())

		got, err := svc.GetPrompts(ctx, connect.NewRequest(&pb.GetPromptsRequest{}))
		require.NoError(t, err)
		require.Len(t, got.Msg.GetPrompts(), 1)
		require.Equal(t, "code", got.Msg.GetPrompts()[0].GetCategory())
	})

	t.Run("a failed write is internal", func(t *testing.T) {
		repo := memorySettings{}.repo()
		repo.CreateSettingFunc = func(context.Context, *db.Setting) error { return errors.New("db down") }
		svc := newSettingsService(t, repo)
		_, err := svc.SavePrompts(settings.AuthedContext(t), connect.NewRequest(&pb.SavePromptsRequest{}))
		require.Equal(t, connect.CodeInternal, svcerr.Code(err))
	})
}
