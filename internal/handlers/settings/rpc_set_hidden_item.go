package settings

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// SetHiddenItem toggles the hidden state of a workflow or preset
func (s *Service) SetHiddenItem(ctx context.Context, req *reliantv1.SetHiddenItemRequest) (*reliantv1.SetHiddenItemResponse, error) {
	userID := auth.MustGetUserID(ctx)

	// Validate item type
	if req.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW && req.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET {
		return nil, svcerr.InvalidArgument("item_type must be 'workflow' or 'preset'")
	}

	// Validate slug
	if req.Slug == "" {
		return nil, svcerr.InvalidArgument("slug cannot be empty")
	}

	// Update visibility override
	// hidden=true means is_visible=false, hidden=false means is_visible=true
	isVisible := !req.Hidden
	if err := s.deps.Repo.SetVisibilityOverride(ctx, userID, int32(req.ItemType), req.Slug, isVisible); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update hidden state"))
	}

	action := "hidden"
	if !req.Hidden {
		action = "shown"
	}

	return &reliantv1.SetHiddenItemResponse{
		Success: true,
		Message: fmt.Sprintf("%s %s successfully %s", req.ItemType, req.Slug, action),
	}, nil
}
