package core

import (
	"time"

	"github.com/reliant-labs/reliant/internal/attachment"
)

// Attachment represents a file attachment.
//
// This lives in core so internal/store can return it without importing the
// db facade package.
type Attachment struct {
	ID       string
	UserID   string
	Filename string
	Size     int64
	MimeType string
	FileHash *string
	FilePath string
	// AttachmentType is an attachment.AttachmentType: "image",
	// "file_reference", "document", "video" or "display".
	AttachmentType string
	Content        []byte // File content bytes (nil for file_references)
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SourcePath is the file on the user's machine a tool presented this
// attachment from, or "". Only an attachment.TypeDisplay row has one: there
// FilePath is the absolute path present_image read, kept (as a file
// reference keeps its original path) so the client can open the file. Every
// other type's FilePath is a storage name that means nothing to a client.
func (a *Attachment) SourcePath() string {
	if a == nil || a.AttachmentType != string(attachment.TypeDisplay) {
		return ""
	}
	return a.FilePath
}
