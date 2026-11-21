package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

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
