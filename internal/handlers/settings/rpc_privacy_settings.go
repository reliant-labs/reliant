package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
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

// UpdatePrivacySettings updates user privacy settings
func (s *Service) UpdatePrivacySettings(ctx context.Context, req *connect.Request[reliantv1.UpdatePrivacySettingsRequest]) (*connect.Response[reliantv1.UpdatePrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get current settings
	analyticsEnabled, crashReportingEnabled, err := s.deps.Repo.GetPrivacySettings(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update privacy settings"))
	}

	// Update only the fields that were provided
	if req.Msg.AnalyticsEnabled != nil {
		analyticsEnabled = *req.Msg.AnalyticsEnabled
	}
	if req.Msg.CrashReportingEnabled != nil {
		crashReportingEnabled = *req.Msg.CrashReportingEnabled
	}

	// Save the updated settings
	if err := s.deps.Repo.SetPrivacySettings(ctx, userID, analyticsEnabled, crashReportingEnabled); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save privacy settings"))
	}

	logging.InfoContext(ctx, "Privacy settings updated", "userID", userID, "analyticsEnabled", analyticsEnabled, "crashReportingEnabled", crashReportingEnabled)

	return connect.NewResponse(&reliantv1.UpdatePrivacySettingsResponse{
		Success:               true,
		Message:               "Privacy settings updated successfully",
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
		RequiresRestart:       false,
	}), nil
}

// TrackPageVisited tracks a page visit for analytics
func (s *Service) TrackPageVisited(ctx context.Context, req *connect.Request[reliantv1.TrackPageVisitedRequest]) (*connect.Response[reliantv1.TrackPageVisitedResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get analytics client that respects user's privacy settings
	analyticsClient := analytics.ForUser(ctx, userID)

	metrics := analytics.PageVisitedMetrics{
		PageName: req.Msg.PageName,
	}
	if req.Msg.PreviousPage != nil {
		metrics.PreviousPage = *req.Msg.PreviousPage
	}

	analyticsClient.TrackPageVisited(metrics)

	return connect.NewResponse(&reliantv1.TrackPageVisitedResponse{
		Success: true,
	}), nil
}

// TrackOnboardingEvent tracks an onboarding flow event for analytics.
// TODO: Uncomment after running `buf generate` to regenerate proto types.
//
// func (s *Service) TrackOnboardingEvent(ctx context.Context, req *connect.Request[reliantv1.TrackOnboardingEventRequest]) (*connect.Response[reliantv1.TrackOnboardingEventResponse], error) {
// 	userID := auth.MustGetUserID(ctx)
// 	analyticsClient := analytics.ForUser(ctx, userID)
//
// 	eventType := analytics.EventType(req.Msg.EventType)
// 	// Validate event type
// 	validTypes := map[analytics.EventType]bool{
// 		analytics.EventOnboardingStarted:       true,
// 		analytics.EventOnboardingStepCompleted: true,
// 		analytics.EventOnboardingStepSkipped:   true,
// 		analytics.EventOnboardingCompleted:     true,
// 		analytics.EventOnboardingSkipped:       true,
// 	}
// 	if !validTypes[eventType] {
// 		return nil, svcerr.InvalidArgument(fmt.Sprintf("invalid onboarding event type: %s", req.Msg.EventType))
// 	}
//
// 	metrics := analytics.OnboardingMetrics{
// 		TotalSteps:     int(req.Msg.TotalSteps),
// 		StepsCompleted: int(req.Msg.StepsCompleted),
// 		StepsSkipped:   int(req.Msg.StepsSkipped),
// 	}
// 	if req.Msg.StepId != nil {
// 		metrics.StepID = *req.Msg.StepId
// 	}
// 	if req.Msg.StepName != nil {
// 		metrics.StepName = *req.Msg.StepName
// 	}
//
// 	analyticsClient.TrackOnboardingEvent(eventType, metrics)
//
// 	return connect.NewResponse(&reliantv1.TrackOnboardingEventResponse{
// 		Success: true,
// 	}), nil
// }
