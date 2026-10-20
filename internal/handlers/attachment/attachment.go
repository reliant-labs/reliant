// Copyright (c) 2025 Reliant Labs
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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// defaultMaxFileSize is the largest upload accepted when Deps.MaxFileSize
// does not say otherwise: 10 MB.
const defaultMaxFileSize = int64(10 * 1024 * 1024)

// ============================================================================
// Helper Methods
// ============================================================================

// dbAttachmentToProto converts a db.Attachment to protobuf AttachmentInfo
func dbAttachmentToProto(a *db.Attachment) *reliantv1.AttachmentInfo {
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
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("filename is required"))
	}

	if len(content) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file content is required"))
	}

	// Validate file type - UploadAttachment accepts images and text files
	attachmentType := attachment.GetAttachmentType(filename)
	if attachmentType == attachment.TypeUnsupported {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported file type: %s. Supported: images (jpg, png, gif, webp) and text files (md, txt, json, code files)", filepath.Ext(filename)))
	}

	// Check file size
	if int64(len(content)) > s.deps.MaxFileSize {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("file too large: maximum size is %d bytes", s.deps.MaxFileSize))
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
		logging.ErrorContext(ctx, "Failed to save attachment to database", "error", err, "attachmentID", attachmentID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save attachment"))
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

// GetAttachment retrieves an attachment by ID (includes file content)
func (s *Service) GetAttachment(
	ctx context.Context,
	req *connect.Request[reliantv1.GetAttachmentRequest],
) (*connect.Response[reliantv1.GetAttachmentResponse], error) {
	userID := auth.MustGetUserID(ctx)
	attachmentID := req.Msg.AttachmentId

	if attachmentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("attachment_id is required"))
	}

	dbAttachment, err := s.deps.Repo.GetAttachment(ctx, attachmentID)
	if err != nil || dbAttachment == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("attachment not found"))
	}

	// Verify ownership
	if dbAttachment.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("attachment not found"))
	}

	attachmentInfo := dbAttachmentToProto(dbAttachment)

	return connect.NewResponse(&reliantv1.GetAttachmentResponse{
		Attachment: attachmentInfo,
		Content:    dbAttachment.Content,
	}), nil
}

// DeleteAttachment removes an attachment by ID
func (s *Service) DeleteAttachment(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteAttachmentRequest],
) (*connect.Response[reliantv1.DeleteAttachmentResponse], error) {
	userID := auth.MustGetUserID(ctx)
	attachmentID := req.Msg.AttachmentId

	if attachmentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("attachment_id is required"))
	}

	// Verify the attachment exists and belongs to this user
	dbAttachment, err := s.deps.Repo.GetAttachment(ctx, attachmentID)
	if err != nil || dbAttachment == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("attachment not found"))
	}
	if dbAttachment.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("attachment not found"))
	}

	if err := s.deps.Repo.DeleteAttachment(ctx, attachmentID); err != nil {
		logging.ErrorContext(ctx, "Failed to delete attachment from database", "error", err, "attachmentID", attachmentID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete attachment"))
	}

	logging.DebugContext(ctx, "Attachment deleted via gRPC", "attachmentID", attachmentID, "userID", userID)

	return connect.NewResponse(&reliantv1.DeleteAttachmentResponse{
		Message: "Attachment deleted successfully",
	}), nil
}

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
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file_path is required"))
	}

	if len(content) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("content is required: daemon must read the file and send its bytes"))
	}

	// Use filename from request, or extract from path
	if filename == "" {
		filename = filepath.Base(filePath)
	}

	// Validate file type
	attachmentType := attachment.GetAttachmentType(filename)
	if attachmentType != attachment.TypeFileReference {
		if attachmentType == attachment.TypeImage {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("use UploadAttachment for image files, not CreateFileReference"))
		}
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported file type: %s", filepath.Ext(filename)))
	}

	// Check file size
	if int64(len(content)) > s.deps.MaxFileSize {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("file too large: %d bytes exceeds maximum of %d bytes", len(content), s.deps.MaxFileSize))
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
	dbAttachment := &db.Attachment{
		ID:             attachmentID,
		UserID:         userID,
		Filename:       filename,
		Size:           int64(len(content)),
		MimeType:       mimeType,
		FileHash:       &fileHash,
		FilePath:       filePath, // Keep original path as metadata for display
		AttachmentType: string(attachment.TypeFileReference),
		Content:        content,
	}

	if err := s.deps.Repo.CreateAttachment(ctx, dbAttachment); err != nil {
		logging.ErrorContext(ctx, "Failed to save file reference to database", "error", err, "attachmentID", attachmentID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create file reference"))
	}

	logging.DebugContext(ctx, "File reference created via gRPC",
		"attachmentID", attachmentID,
		"size", len(content),
		"mime_type", mimeType,
		"userID", userID)

	// Create response
	attachmentInfo := &reliantv1.AttachmentInfo{
		Id:             attachmentID,
		UserId:         userID,
		Filename:       filename,
		Size:           int64(len(content)),
		MimeType:       mimeType,
		FileHash:       &fileHash,
		FilePath:       filePath,
		CreatedAt:      time.Now().Format(time.RFC3339),
		UpdatedAt:      time.Now().Format(time.RFC3339),
		Url:            fmt.Sprintf("/api/attachments/%s", attachmentID),
		AttachmentType: string(attachment.TypeFileReference),
	}

	return connect.NewResponse(&reliantv1.CreateFileReferenceResponse{
		Attachment: attachmentInfo,
	}), nil
}
