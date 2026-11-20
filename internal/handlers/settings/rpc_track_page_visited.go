package settings

import (
	"context"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TrackPageVisited tracks a page visit for analytics
func (s *Service) TrackPageVisited(ctx context.Context, req *reliantv1.TrackPageVisitedRequest) (*reliantv1.TrackPageVisitedResponse, error) {
	userID := auth.MustGetUserID(ctx)

	// Get analytics client that respects user's privacy settings
	analyticsClient := analytics.ForUser(ctx, userID)

	metrics := analytics.PageVisitedMetrics{
		PageName: req.PageName,
	}
	if req.PreviousPage != nil {
		metrics.PreviousPage = *req.PreviousPage
	}

	analyticsClient.TrackPageVisited(metrics)

	return &reliantv1.TrackPageVisitedResponse{
		Success: true,
	}, nil
}
