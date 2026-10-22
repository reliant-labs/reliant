package settings_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestGetPrivacySettings_Generated(t *testing.T) {
	t.Parallel()

	t.Run("the caller's stored choices", func(t *testing.T) {
		repo := memorySettings{}.repo()
		repo.GetPrivacySettingsFunc = func(_ context.Context, userID string) (bool, bool, error) {
			require.Equal(t, "test-user", userID)
			return false, true, nil
		}
		resp, err := newSettingsService(t, repo).GetPrivacySettings(settings.AuthedContext(t), connect.NewRequest(&pb.GetPrivacySettingsRequest{}))
		require.NoError(t, err)
		require.False(t, resp.Msg.GetAnalyticsEnabled())
		require.True(t, resp.Msg.GetCrashReportingEnabled())
	})

	t.Run("a failed read is internal", func(t *testing.T) {
		repo := memorySettings{}.repo()
		repo.GetPrivacySettingsFunc = func(context.Context, string) (bool, bool, error) { return false, false, errors.New("db down") }
		_, err := newSettingsService(t, repo).GetPrivacySettings(settings.AuthedContext(t), connect.NewRequest(&pb.GetPrivacySettingsRequest{}))
		require.Equal(t, connect.CodeInternal, svcerr.Code(err))
	})
}
