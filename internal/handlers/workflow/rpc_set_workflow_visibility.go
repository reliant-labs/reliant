package workflow

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// SetWorkflowVisibility updates the visibility status of a workflow (hidden/visible)
func (s *Service) SetWorkflowVisibility(
	ctx context.Context,
	req *connect.Request[reliantv1.SetWorkflowVisibilityRequest],
) (*connect.Response[reliantv1.SetWorkflowVisibilityResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.Slug == "" {
		return nil, svcerr.InvalidArgument("slug is required")
	}

	userID := auth.MustGetUserID(ctx)

	// Get the workflow draft by slug
	draft, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, req.Msg.Slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
	}
	if draft == nil {
		return nil, svcerr.NotFound("workflow")
	}

	// Update hidden status
	updatedDraft, err := s.deps.Drafts.SetWorkflowDraftHidden(ctx, draft.ID, req.Msg.IsHidden)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow visibility"))
	}

	item, err := userWorkflowListItem(updatedDraft, s.validateWorkflowDefinition(ctx, userID, []byte(updatedDraft.Definition)))
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse stored workflow"))
	}

	return connect.NewResponse(&reliantv1.SetWorkflowVisibilityResponse{
		Success:  true,
		Message:  fmt.Sprintf("Workflow visibility updated to hidden=%v", req.Msg.IsHidden),
		Workflow: item,
	}), nil
}
