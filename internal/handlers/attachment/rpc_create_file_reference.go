package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// CreateFileReference creates a reference to a local file (for text files).
// The daemon/frontend reads the file and sends content bytes in the request
// so the server never needs filesystem access.
func (s *Service) CreateFileReference(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateFileReferenceRequest],
) (*connect.Response[reliantv1.CreateFileReferenceResponse], error) {
	userID := auth.MustGetUserID(ctx)

	filePath := req.Msg.FilePath
	filename := req.Msg.Filename
	content := req.Msg.Content

	// Validate required fields
	if filePath == "" {
		return nil, svcerr.InvalidArgument("file_path is required")
	}

	if len(content) == 0 {
		return nil, svcerr.InvalidArgument("content is required: daemon must read the file and send its bytes")
	}

	// Use filename from request, or extract from path
	if filename == "" {
		filename = filepath.Base(filePath)
	}

	// Validate file type
	attachmentType := attachment.GetAttachmentType(filename)
	if attachmentType != attachment.TypeFileReference {
		if attachmentType == attachment.TypeImage {
			return nil, svcerr.InvalidArgument("use UploadAttachment for image files, not CreateFileReference")
		}
		return nil, svcerr.InvalidArgument(fmt.Sprintf("unsupported file type: %s", filepath.Ext(filename)))
	}

	// Check file size
	if int64(len(content)) > s.maxFileSize {
		return nil, svcerr.ResourceExhausted(fmt.Sprintf("file too large: %d bytes exceeds maximum of %d bytes", len(content), s.maxFileSize))
	}

	// The org stamped on it, verified before anything is stored.
	orgID, err := s.uploadOrg(ctx, userID, req.Msg.OrgId)
	if err != nil {
		return nil, err
	}

	// Get MIME type
	ext := filepath.Ext(filename)
	mimeType := attachment.GetMimeType(ext)

	// Generate unique attachment ID
	attachmentID := uuid.New().String()

	// Compute SHA-256 hash from content bytes
	hash := sha256.Sum256(content)
	fileHash := hex.EncodeToString(hash[:])

	// Store file reference with content in database
	dbAttachment := &core.Attachment{
		ID:             attachmentID,
		UserID:         userID,
		Filename:       filename,
		Size:           int64(len(content)),
		MimeType:       mimeType,
		FileHash:       &fileHash,
		FilePath:       filePath, // Keep original path as metadata for display
		AttachmentType: string(attachment.TypeFileReference),
		Content:        content,
		OrgID:          orgID,
	}

	if err := s.deps.Attachments.InsertAttachment(ctx, dbAttachment); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create file reference"))
	}

	logging.DebugContext(ctx, "File reference created via gRPC",
		"attachmentID", attachmentID,
		"size", len(content),
		"mime_type", mimeType,
		"userID", userID)

	// Create response
	return connect.NewResponse(&reliantv1.CreateFileReferenceResponse{
		Attachment: attachmentToWire(dbAttachment),
	}), nil
}
