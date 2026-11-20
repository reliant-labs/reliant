package inbox

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// DismissInboxItem hides items from the caller's inbox. Hiding an approval or
// a question does not resolve it; the run still waits on it.
func (s *Service) DismissInboxItem(
	ctx context.Context,
	req *reliantv1.DismissInboxItemRequest,
) (*reliantv1.DismissInboxItemResponse, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if err := validateInboxItemIDs(req.ItemIds); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.DismissInboxItems(ctx, userID, impliedDismissals(req.ItemIds), time.Now().UTC()); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to dismiss inbox item"))
	}
	return &reliantv1.DismissInboxItemResponse{}, nil
}
