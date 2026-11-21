package inbox

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// RestoreInboxItem undoes DismissInboxItem (the Undo on the dismiss toast).
func (s *Service) RestoreInboxItem(
	ctx context.Context,
	req *connect.Request[reliantv1.RestoreInboxItemRequest],
) (*connect.Response[reliantv1.RestoreInboxItemResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if err := validateInboxItemIDs(req.Msg.ItemIds); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.RestoreInboxItems(ctx, userID, impliedDismissals(req.Msg.ItemIds)); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to restore inbox item"))
	}
	return connect.NewResponse(&reliantv1.RestoreInboxItemResponse{}), nil
}
