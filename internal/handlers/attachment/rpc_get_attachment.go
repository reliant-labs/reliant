package attachment

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ============================================================================
// Helper Methods
// ============================================================================

// dbAttachmentToProto converts a core.Attachment to protobuf AttachmentInfo
func dbAttachmentToProto(a *core.Attachment) *reliantv1.AttachmentInfo {
	info := &reliantv1.AttachmentInfo{
		Id:             a.ID,
		UserId:         a.UserID,
		Filename:       a.Filename,
		Size:           a.Size,
		MimeType:       a.MimeType,
		FilePath:       a.FilePath,
		CreatedAt:      a.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      a.UpdatedAt.Format(time.RFC3339),
		Url:            fmt.Sprintf("/api/attachments/%s", a.ID),
		AttachmentType: a.AttachmentType,
	}

	if a.FileHash != nil {
		info.FileHash = proto.String(*a.FileHash)
	}

	return info
}

// GetAttachment retrieves an attachment by ID (includes file content)
func (s *Service) GetAttachment(
	ctx context.Context,
	req *connect.Request[reliantv1.GetAttachmentRequest],
) (*connect.Response[reliantv1.GetAttachmentResponse], error) {
	userID := auth.MustGetUserID(ctx)
	attachmentID := req.Msg.AttachmentId

	if attachmentID == "" {
		return nil, svcerr.InvalidArgument("attachment_id is required")
	}

	dbAttachment, err := s.deps.Attachments.GetAttachment(ctx, attachmentID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get attachment"))
	}
	if dbAttachment == nil {
		return nil, svcerr.NotFound("attachment")
	}

	// Its uploader, or the owner of a chat a member attached it in.
	if !canRead(ctx, s.deps.Attachments, userID, dbAttachment) {
		return nil, svcerr.NotFound("attachment")
	}

	attachmentInfo := dbAttachmentToProto(dbAttachment)

	return connect.NewResponse(&reliantv1.GetAttachmentResponse{
		Attachment: attachmentInfo,
		Content:    dbAttachment.Content,
	}), nil
}
