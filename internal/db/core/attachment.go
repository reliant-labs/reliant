package core

import (
	"context"
	"time"
)

// Attachment represents a file attachment.
//
// This lives in core so driver store packages can share a common contract
// without importing the db facade package.
type Attachment struct {
	ID             string
	UserID         string
	Filename       string
	Size           int64
	MimeType       string
	FileHash       *string
	FilePath       string
	AttachmentType string // "image" or "file_reference"
	Content        []byte // File content bytes (nil for file_references)
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AttachmentStore is the shared contract for attachment persistence across drivers.
type AttachmentStore interface {
	CreateAttachment(ctx context.Context, attachment *Attachment) error
	GetAttachment(ctx context.Context, id string) (*Attachment, error)
	GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*Attachment, error)
	DeleteAttachment(ctx context.Context, id string) error
}
