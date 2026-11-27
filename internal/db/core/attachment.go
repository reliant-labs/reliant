package core

import "time"

// Attachment represents a file attachment.
//
// This lives in core so internal/store can return it without importing the
// db facade package.
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
	// OrgID is the organization the attachment was made in, or nil: made in
	// no org, or before attachments recorded one. An org-scoped read
	// (store.GetAttachmentForUserInOrg) never matches a nil one.
	OrgID     *string
	CreatedAt time.Time
	UpdatedAt time.Time
}
