package settings_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdatePrivacySettings_Generated(t *testing.T) {
	t.Parallel()

	t.Run("only the choices set change", func(t *testing.T) {
		analytics, crash := true, true
		repo := memorySettings{}.repo()
		repo.GetPrivacySettingsFunc = func(context.Context, string) (bool, bool, error) { return analytics, crash, nil }
		repo.SetPrivacySettingsFunc = func(_ context.Context, _ string, a, c bool) error {
			analytics, crash = a, c
			return nil
		}
		resp, err := newSettingsService(t, repo).UpdatePrivacySettings(settings.AuthedContext(t),
			&pb.UpdatePrivacySettingsRequest{AnalyticsEnabled: ptr.Of(false)})
		require.NoError(t, err)
		require.False(t, resp.GetAnalyticsEnabled())
		require.True(t, resp.GetCrashReportingEnabled())
		require.False(t, analytics)
		require.True(t, crash)
	})

	t.Run("a failed read is internal", func(t *testing.T) {
		repo := memorySettings{}.repo()
		repo.GetPrivacySettingsFunc = func(context.Context, string) (bool, bool, error) { return false, false, errors.New("db down") }
		_, err := newSettingsService(t, repo).UpdatePrivacySettings(settings.AuthedContext(t),
			&pb.UpdatePrivacySettingsRequest{AnalyticsEnabled: ptr.Of(false)})
		require.Equal(t, connect.CodeInternal, svcerr.CodeV2(err))
	})
}
