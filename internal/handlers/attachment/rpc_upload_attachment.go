package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// RPC Handlers
// ============================================================================

// UploadAttachment uploads a new binary file attachment (for images and text files)
func (s *Service) UploadAttachment(
	ctx context.Context,
	req *connect.Request[reliantv1.UploadAttachmentRequest],
) (*connect.Response[reliantv1.UploadAttachmentResponse], error) {
	userID := auth.MustGetUserID(ctx)

	filename := req.Msg.Filename
	content := req.Msg.Content
	mimeType := req.Msg.MimeType

	// Validate required fields
	if filename == "" {
		return nil, svcerr.InvalidArgument("filename is required")
	}

	if len(content) == 0 {
		return nil, svcerr.InvalidArgument("file content is required")
	}

	// Validate file type - UploadAttachment accepts images and text files
	attachmentType := attachment.GetAttachmentType(filename)
	if attachmentType == attachment.TypeUnsupported {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("unsupported file type: %s. Supported: images (jpg, png, gif, webp) and text files (md, txt, json, code files)", filepath.Ext(filename)))
	}

	// Check file size
	if int64(len(content)) > s.maxFileSize {
		return nil, svcerr.ResourceExhausted(fmt.Sprintf("file too large: maximum size is %d bytes", s.maxFileSize))
	}

	// Default MIME type
	if mimeType == "" {
		mimeType = attachment.GetMimeType(filepath.Ext(filename))
	}

	// Generate unique attachment ID
	attachmentID := uuid.New().String()

	// Compute SHA-256 hash directly from content bytes
	hash := sha256.Sum256(content)
	fileHash := hex.EncodeToString(hash[:])

	// Build a relative path for the proto response (no longer used for reading)
	ext := filepath.Ext(filename)
	relativePath := filepath.Join(userID, attachmentID+ext)

	// Store attachment content + metadata in database
	dbAttachment := &db.Attachment{
		ID:             attachmentID,
		UserID:         userID,
		Filename:       filename,
		Size:           int64(len(content)),
		MimeType:       mimeType,
		FileHash:       &fileHash,
		FilePath:       relativePath,
		AttachmentType: string(attachmentType),
		Content:        content,
	}

	if err := s.deps.Repo.CreateAttachment(ctx, dbAttachment); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save attachment"))
	}

	logging.DebugContext(ctx, "File uploaded successfully via gRPC",
		"attachmentID", attachmentID,
		"size", len(content),
		"mime_type", mimeType,
		"hash", fileHash,
		"attachmentType", attachmentType,
		"userID", userID)

	attachmentInfo := &reliantv1.AttachmentInfo{
		Id:             attachmentID,
		UserId:         userID,
		Filename:       filename,
		Size:           int64(len(content)),
		MimeType:       mimeType,
		FileHash:       proto.String(fileHash),
		FilePath:       relativePath,
		CreatedAt:      time.Now().Format(time.RFC3339),
		UpdatedAt:      time.Now().Format(time.RFC3339),
		Url:            fmt.Sprintf("/api/attachments/%s", attachmentID),
		AttachmentType: string(attachmentType),
	}

	return connect.NewResponse(&reliantv1.UploadAttachmentResponse{
		Attachment: attachmentInfo,
	}), nil
}
