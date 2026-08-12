// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/pdfutil"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// ReadAttachmentToolName is the registered name for the read_attachment tool.
const ReadAttachmentToolName = ToolReadAttachment

// ReadAttachmentParams identifies the attachment to read and, for PDFs, the
// page range to return.
type ReadAttachmentParams struct {
	AttachmentID string `json:"attachment_id" jsonschema:"required,description=The id of the attachment to read (shown in the attachment reference in the conversation)."`
	Pages        string `json:"pages,omitempty" jsonschema:"description=PDF attachments only. Page range to read (e.g. '1-5'\\, '3'\\, '10-20'). Maximum 20 pages per request. Required for PDFs larger than 10 pages; ignored for non-PDF attachments."`
}

const readAttachmentDescription = `Read the contents of a file the user attached to the conversation.

WHEN TO USE:
- When the conversation references an attachment (by id) whose contents you have not yet seen.
- Specifically for PDF attachments: their pages are read on demand through this tool.

HOW TO USE:
- Provide the attachment_id from the attachment reference.
- For PDFs: use the pages parameter to read a page range (e.g. "1-5"). PDFs larger than 10 pages require a page range; max 20 pages per request.

NOTES:
- PDF pages are returned as a native document block the model can read directly (text and layout).
- Image and text attachments are already provided inline in the conversation and do not need this tool.`

// ReadAttachmentTool reads attachment bytes straight from the database and, for
// PDFs, paginates them with pdfutil. It is a server-run tool because it needs
// database access; the bytes never touch the daemon filesystem.
type ReadAttachmentTool struct {
	repo db.Repository
}

// NewReadAttachmentTool creates the read_attachment tool.
func NewReadAttachmentTool(repo db.Repository) Tool {
	return NewToolWrapper(&ReadAttachmentTool{repo: repo})
}

func (t *ReadAttachmentTool) Name() string {
	return ReadAttachmentToolName
}

func (t *ReadAttachmentTool) Description() string {
	return readAttachmentDescription
}

func (t *ReadAttachmentTool) IsReadOnly() bool {
	return true
}

func (t *ReadAttachmentTool) RequiresPermission(params ReadAttachmentParams) (bool, error) {
	return false, nil
}

// Execute reads the attachment and returns its contents. For PDFs it paginates
// exactly like the view tool: a page range yields a document block; a large PDF
// with no range yields a text prompt asking for one; a small PDF is returned whole.
func (t *ReadAttachmentTool) Execute(rctx *rctx.ToolContext, params ReadAttachmentParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("attachment access is unavailable in this context"), nil
	}
	if params.AttachmentID == "" {
		return NewTextErrorResponse("attachment_id is required"), nil
	}

	att, err := t.repo.GetAttachment(rctx.Context, params.AttachmentID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to load attachment %s: %v", params.AttachmentID, err)), nil
	}
	if att == nil {
		return NewTextErrorResponse(fmt.Sprintf("Attachment %s not found", params.AttachmentID)), nil
	}
	if len(att.Content) == 0 {
		return NewTextErrorResponse(fmt.Sprintf("Attachment %s has no stored content", params.AttachmentID)), nil
	}

	if strings.ToLower(filepath.Ext(att.Filename)) == ".pdf" {
		return t.readPDF(att, params.Pages)
	}

	// Non-PDF: return the raw content as text. Text attachments are already
	// inlined in the conversation, so this is a best-effort fallback.
	return NewTextResponse(fmt.Sprintf("Contents of %s:\n```\n%s\n```", att.Filename, string(att.Content))), nil
}

func (t *ReadAttachmentTool) readPDF(att *db.Attachment, pages string) (ToolResponse, error) {
	if pages != "" {
		out, err := pdfutil.ExtractPages(att.Content, pages)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to read pages %q of %s: %v", pages, att.Filename, err)), nil
		}
		return NewImageResponse(
			fmt.Sprintf("PDF attachment: %s (pages %s)", att.Filename, pages),
			[]message.BinaryContent{{
				Path:     att.Filename,
				MIMEType: "application/pdf",
				Data:     out,
			}},
		), nil
	}

	pageCount, err := pdfutil.PageCount(att.Content)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to read PDF attachment %s: %v", att.Filename, err)), nil
	}

	if pageCount > PDFAutoInlinePageLimit {
		return NewTextResponse(fmt.Sprintf(
			"PDF attachment: %s has %d pages. Call read_attachment again with the pages parameter to read a range "+
				"(e.g. pages=%q). Maximum %d pages per request.",
			att.Filename, pageCount, fmt.Sprintf("1-%d", PDFAutoInlinePageLimit), pdfutil.MaxPagesPerRequest,
		)), nil
	}

	// Small PDF — return the whole document.
	return NewImageResponse(
		fmt.Sprintf("PDF attachment: %s (%d pages)", att.Filename, pageCount),
		[]message.BinaryContent{{
			Path:     att.Filename,
			MIMEType: "application/pdf",
			Data:     att.Content,
		}},
	), nil
}
