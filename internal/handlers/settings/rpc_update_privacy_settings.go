package settings

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// UpdatePrivacySettings updates user privacy settings
func (s *Service) UpdatePrivacySettings(ctx context.Context, req *reliantv1.UpdatePrivacySettingsRequest) (*reliantv1.UpdatePrivacySettingsResponse, error) {
	userID := auth.MustGetUserID(ctx)

	// Get current settings
	analyticsEnabled, crashReportingEnabled, err := s.deps.Repo.GetPrivacySettings(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update privacy settings"))
	}

	// Update only the fields that were provided
	if req.AnalyticsEnabled != nil {
		analyticsEnabled = *req.AnalyticsEnabled
	}
	if req.CrashReportingEnabled != nil {
		crashReportingEnabled = *req.CrashReportingEnabled
	}

	// Save the updated settings
	if err := s.deps.Repo.SetPrivacySettings(ctx, userID, analyticsEnabled, crashReportingEnabled); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save privacy settings"))
	}

	logging.InfoContext(ctx, "Privacy settings updated", "userID", userID, "analyticsEnabled", analyticsEnabled, "crashReportingEnabled", crashReportingEnabled)

	return &reliantv1.UpdatePrivacySettingsResponse{
		Success:               true,
		Message:               "Privacy settings updated successfully",
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
		RequiresRestart:       false,
	}, nil
}
