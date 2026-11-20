package settings

import (
	"context"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// These rpcs were never implemented. Each answers exactly as before, through
// the embedded Unimplemented handler rather than forge's scaffold stub.

// TrackOnboardingEvent was never implemented (a commented-out draft follows);
// it answers exactly as before.
func (s *Service) TrackOnboardingEvent(ctx context.Context, req *pb.TrackOnboardingEventRequest) (*pb.TrackOnboardingEventResponse, error) {
	return s.UnimplementedSettingsServiceHandler.TrackOnboardingEvent(ctx, req)
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
