package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ============================================================================
// Sub-phase 6e: Privacy Settings
// ============================================================================

// GetPrivacySettings retrieves user privacy settings
func (s *Service) GetPrivacySettings(ctx context.Context, req *connect.Request[reliantv1.GetPrivacySettingsRequest]) (*connect.Response[reliantv1.GetPrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	analyticsEnabled, crashReportingEnabled, err := s.deps.Repo.GetPrivacySettings(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get privacy settings"))
	}

	return connect.NewResponse(&reliantv1.GetPrivacySettingsResponse{
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
	}), nil
}
