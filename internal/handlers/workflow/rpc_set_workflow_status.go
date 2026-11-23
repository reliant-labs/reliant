package workflow

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/logging"
)

// SetWorkflowStatus moves a stored workflow between draft and complete.
// Marking complete validates the CURRENT definition exactly as run start will
// and is rejected with the errors when it is invalid. Moving to draft always
// succeeds — it is how a user takes a complete workflow back to make edits
// that are invalid along the way.
func (s *Service) SetWorkflowStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.SetWorkflowStatusRequest],
) (*connect.Response[reliantv1.SetWorkflowStatusResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.DraftId == "" {
		return nil, svcerr.InvalidArgument("draft_id is required")
	}
	var target db.WorkflowDraftStatus
	switch req.Msg.Status {
	case reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE:
		target = db.WorkflowDraftStatusComplete
	case reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT:
		target = db.WorkflowDraftStatusDraft
	default:
		return nil, svcerr.InvalidArgument("status must be DRAFT or COMPLETE")
	}

	userID := auth.MustGetUserID(ctx)
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	draft, err := s.deps.Drafts.GetWorkflowDraft(ctx, req.Msg.DraftId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
	}
	if !authz.CanWriteWorkflow(ctx, userID, draft) {
		return nil, svcerr.NotFound("workflow")
	}
	if req.Msg.ExpectedVersion != nil && draft.Version != *req.Msg.ExpectedVersion {
		return nil, svcerr.WithReason(svcerr.Aborted(fmt.Sprintf(
			"workflow was modified since you last loaded it (expected version: %d, current version: %d) - please reload and retry",
			*req.Msg.ExpectedVersion, draft.Version)), errreason.StaleRevision)
	}

	check := s.validateWorkflowDefinition(ctx, userID, []byte(draft.Definition))
	if target == db.WorkflowDraftStatusComplete && !check.valid() {
		activity.Skip(ctx) // refused: the status did not change
		return connect.NewResponse(&reliantv1.SetWorkflowStatusResponse{
			Success:          false,
			Message:          "Not marked complete: fix the validation errors first — " + check.summary(),
			Status:           draftStatusToProto(draft.Status),
			ValidationErrors: check.protoErrors(true),
			Version:          draft.Version,
			Slug:             draft.Slug,
		}), nil
	}

	updated := draft
	if draft.Status == target {
		activity.Skip(ctx) // already there: nothing changed
	} else {
		updated, err = s.deps.Drafts.SetWorkflowDraftStatus(ctx, draft.ID, target)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow status"))
		}
		logging.InfoContext(ctx, "Workflow status changed", "draft_id", draft.ID, "slug", draft.Slug, "from", draft.Status, "to", target, "user_id", userID)
	}

	message := "Workflow marked complete — it can now be run"
	if target == db.WorkflowDraftStatusDraft {
		message = "Workflow moved to draft — it will not run until it is marked complete again"
	}
	return connect.NewResponse(&reliantv1.SetWorkflowStatusResponse{
		Success:          true,
		Message:          message,
		Status:           draftStatusToProto(updated.Status),
		ValidationErrors: check.protoErrors(true),
		Version:          updated.Version,
		Slug:             updated.Slug,
	}), nil
}
