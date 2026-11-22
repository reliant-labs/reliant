package attachment

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DeleteAttachment removes an attachment by ID
func (s *Service) DeleteAttachment(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteAttachmentRequest],
) (*connect.Response[reliantv1.DeleteAttachmentResponse], error) {
	userID := auth.MustGetUserID(ctx)
	attachmentID := req.Msg.AttachmentId

	if attachmentID == "" {
		return nil, svcerr.InvalidArgument("attachment_id is required")
	}

	// Verify the attachment exists and belongs to this user
	dbAttachment, err := s.deps.Attachments.GetAttachment(ctx, attachmentID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get attachment"))
	}
	if dbAttachment == nil {
		return nil, svcerr.NotFound("attachment")
	}
	if !authz.CanAccessPersonal(userID, dbAttachment.UserID) {
		return nil, svcerr.NotFound("attachment")
	}

	if err := s.deps.Attachments.RemoveAttachment(ctx, attachmentID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete attachment"))
	}

	logging.DebugContext(ctx, "Attachment deleted via gRPC", "attachmentID", attachmentID, "userID", userID)

	return connect.NewResponse(&reliantv1.DeleteAttachmentResponse{
		Message: "Attachment deleted successfully",
	}), nil
}
