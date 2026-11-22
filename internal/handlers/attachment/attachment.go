// Copyright (c) 2025 Reliant Labs
package attachment

import (
	"fmt"

	"google.golang.org/protobuf/types/known/timestamppb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// defaultMaxFileSize is the largest upload accepted when Deps.API
// does not say otherwise: 10 MB.
const defaultMaxFileSize = int64(10 * 1024 * 1024)

// attachmentURL is where a client fetches the attachment with this id. It
// is wire-only (no column holds it), so every rpc that returns an Attachment
// fills it from the id.
func attachmentURL(id string) string {
	return fmt.Sprintf("/api/attachments/%s", id)
}

// attachmentToWire is the wire shape of a stored attachment, without its
// bytes.
func attachmentToWire(a *core.Attachment) *reliantv1.Attachment {
	return &reliantv1.Attachment{
		Id:             a.ID,
		UserId:         a.UserID,
		Filename:       a.Filename,
		Size:           a.Size,
		MimeType:       a.MimeType,
		FileHash:       a.FileHash,
		FilePath:       a.FilePath,
		CreatedAt:      timestamppb.New(a.CreatedAt),
		UpdatedAt:      timestamppb.New(a.UpdatedAt),
		Url:            attachmentURL(a.ID),
		AttachmentType: a.AttachmentType,
	}
}
