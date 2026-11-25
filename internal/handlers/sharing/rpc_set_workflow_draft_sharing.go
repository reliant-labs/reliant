package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

// SetWorkflowDraftSharing is SetChatVisibility for a saved workflow
// (org-sharing.md §8.5): sharing one publishes its definition for the team to
// read and fork; it never lets a viewer run it as its owner.
func (s *Service) SetWorkflowDraftSharing(
	ctx context.Context,
	req *connect.Request[pb.SetWorkflowDraftSharingRequest],
) (*connect.Response[pb.SetWorkflowDraftSharingResponse], error) {
	userID := auth.MustGetUserID(ctx)
	to, err := visibilityFrom(req.Msg.GetVisibility())
	if err != nil {
		return nil, err
	}
	ctx = authz.WithViewerReads(ctx)
	workflow, err := s.deps.WorkflowDrafts.GetWorkflowDraft(ctx, req.Msg.GetWorkflowId())
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, svcerr.NotFound("saved workflow")
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get saved workflow"))
	}
	change, err := authz.WorkflowShareChange(ctx, userID, workflow, to)
	if err != nil {
		return nil, shareRefusal(ctx, "saved workflow", err)
	}
	changed := false
	if authz.WorkflowVisibility(workflow) != to {
		// ReadCommitted, as SetChatVisibility: the UPDATE's row lock and its
		// re-checked predicate serialize a concurrent share and unshare.
		if changed, err = s.applyShareChange(ctx, change); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to change who can read the saved workflow"))
		}
	}
	resp := &pb.SetWorkflowDraftSharingResponse{Changed: changed}
	// The saved workflow as the caller now reads it; one decision shapes it
	// (authz.ReadWorkflowAccess).
	if after, err := s.deps.WorkflowDrafts.GetWorkflowDraft(ctx, workflow.ID); err == nil {
		if access, denied := authz.ReadWorkflowAccess(ctx, userID, after); denied == nil {
			resp.Workflow = sharedWorkflowDraft(after, access.Viewer())
		}
	}
	return connect.NewResponse(resp), nil
}
