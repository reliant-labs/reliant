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
	// OrgID is an organization the owner was a live member of when the
	// attachment was stored (attachments.org_id), or nil: none named or
	// verified, or stored before attachments recorded one. It does not say
	// which chat the attachment came from. An org-scoped read
	// (store.GetAttachmentForUserInOrg) never matches a nil one.
	OrgID     *string
	CreatedAt time.Time
	UpdatedAt time.Time
}
