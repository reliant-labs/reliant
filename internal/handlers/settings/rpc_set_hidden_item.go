package settings

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// SetHiddenItem toggles the hidden state of a workflow or preset
func (s *Service) SetHiddenItem(ctx context.Context, req *connect.Request[reliantv1.SetHiddenItemRequest]) (*connect.Response[reliantv1.SetHiddenItemResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Validate item type
	if req.Msg.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW && req.Msg.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET {
		return nil, svcerr.InvalidArgument("item_type must be 'workflow' or 'preset'")
	}

	// Validate slug
	if req.Msg.Slug == "" {
		return nil, svcerr.InvalidArgument("slug cannot be empty")
	}

	// Update visibility override
	// hidden=true means is_visible=false, hidden=false means is_visible=true
	isVisible := !req.Msg.Hidden
	if err := s.deps.Repo.SetVisibilityOverride(ctx, userID, int32(req.Msg.ItemType), req.Msg.Slug, isVisible); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update hidden state"))
	}

	action := "hidden"
	if !req.Msg.Hidden {
		action = "shown"
	}

	return connect.NewResponse(&reliantv1.SetHiddenItemResponse{
		Success: true,
		Message: fmt.Sprintf("%s %s successfully %s", req.Msg.ItemType, req.Msg.Slug, action),
	}), nil
}
