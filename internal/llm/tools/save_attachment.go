// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// SaveAttachmentToolName is the registered name for the save_attachment tool.
const SaveAttachmentToolName = ToolSaveAttachment

// SaveAttachmentParams names the stored attachment and where to put it.
//
// save_to and repo are spelled and described exactly as on generate_image.
// The two tools are halves of one system — generate then save, or save again
// later — and a model that has learned one set of path semantics should not
// have to learn a second.
type SaveAttachmentParams struct {
	AttachmentID string `json:"attachment_id" jsonschema:"required,description=The id of the attachment to write to disk. Tool results that produce an attachment report this id."`
	SaveTo       string `json:"save_to" jsonschema:"required,description=Path to write the file to on disk. Relative paths resolve against the working directory."`
	Repo         string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo save_to is relative to: 'root' for the project root\\, or a repo name. Omit in single-repo projects or when save_to is absolute."`
}

const saveAttachmentDescription = `Write an already-stored attachment to a file on disk.

WHEN TO USE:
- An image, PDF or file already exists as an attachment and the user now wants
  it as a file in the project.
- A previous generate_image call reported an attachment id but was not given
  save_to, or its save_to write failed.

WHY THIS RATHER THAN REGENERATING:
- The bytes already exist and are addressable. Calling generate_image again
  costs another billed generation AND produces a DIFFERENT image — image models
  are not deterministic, so the file you would write is not the image the user
  approved. Always prefer save_attachment for an image you already have.

THIS IS NOT read_attachment:
- read_attachment pulls the contents into the conversation so you can LOOK at
  them. This tool moves the bytes server-side, straight from storage to the
  file, without routing them through the conversation. Use this one to produce
  a file; use that one to inspect contents.

HOW TO USE:
- Pass the attachment_id and the destination path. Relative paths resolve
  against the working directory; use repo in a multi-repo project.

NOTES:
- Binary content is written byte-for-byte, so images and PDFs stay valid.
- Parent directories are created as needed.`

// SaveAttachmentOutput is the structured result of one save.
type SaveAttachmentOutput struct {
	AttachmentID string `json:"attachment_id"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int    `json:"size"`
	// SavedTo is the resolved ABSOLUTE path. The model supplies a path that is
	// usually relative, so echoing back what it sent would tell it nothing; the
	// resolved path is what it needs to reference the file afterwards.
	SavedTo string `json:"saved_to"`
}

// saveAttachmentTool copies stored attachment bytes onto the user's filesystem.
//
// Server-run: the bytes live in the database, which the daemon cannot reach.
// The file still lands on the user's machine, through the daemon client on the
// tool context — the same route generate_image's save_to takes.
type saveAttachmentTool struct {
	repo db.Repository
}

// NewSaveAttachmentTool creates the save_attachment tool.
func NewSaveAttachmentTool(repo db.Repository) Tool {
	return NewToolWrapper[SaveAttachmentParams, ToolResponse](&saveAttachmentTool{repo: repo})
}

func (t *saveAttachmentTool) Name() string {
	return SaveAttachmentToolName
}

func (t *saveAttachmentTool) Description() string {
	return saveAttachmentDescription
}

func (t *saveAttachmentTool) RequiresPermission(params SaveAttachmentParams) (bool, error) {
	// It writes a file to the user's disk, which is what the permission layer
	// exists for. That it costs nothing to run is irrelevant: the destructive
	// part is the write, not the read.
	return true, nil
}

func (t *saveAttachmentTool) Execute(tc *rctx.ToolContext, params SaveAttachmentParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("attachment access is unavailable in this context"), nil
	}
	attachmentID := strings.TrimSpace(params.AttachmentID)
	if attachmentID == "" {
		return NewTextErrorResponse("attachment_id is required"), nil
	}
	if strings.TrimSpace(params.SaveTo) == "" {
		return NewTextErrorResponse("save_to is required: name the path to write the attachment to"), nil
	}

	att, err := t.repo.GetAttachment(tc.Context, attachmentID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to load attachment %s: %v", attachmentID, err)), nil
	}
	if att == nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"Attachment %s not found. Check the id reported by the tool call that produced it; "+
				"attachment ids are not guessable and are not reusable across users.", attachmentID)), nil
	}
	if len(att.Content) == 0 {
		return NewTextErrorResponse(fmt.Sprintf(
			"Attachment %s (%s) has no stored content, so there is nothing to write. "+
				"File-reference attachments record a path rather than bytes.", attachmentID, att.Filename)), nil
	}

	// Ownership check. The attachment id is the only capability here, so an id
	// that leaked from another conversation must not become a file read. Only
	// enforced when the caller's identity is known: paths that build a tool
	// context by hand have no user on it, and failing closed there would break
	// legitimate callers rather than any attacker.
	if callerID := t.callerUserID(tc); callerID != "" && att.UserID != "" && att.UserID != callerID {
		return NewTextErrorResponse(fmt.Sprintf(
			"Attachment %s belongs to a different user and cannot be saved.", attachmentID)), nil
	}

	if tc.Daemon == nil {
		return NewTextErrorResponse("Writing a file requires a connected daemon, and none is available in this context."), nil
	}

	path := params.SaveTo
	if !filepath.IsAbs(path) {
		workingDir, dirErr := ResolveRepoPath(tc, params.Repo)
		if dirErr != nil {
			return NewTextErrorResponse(fmt.Sprintf("Couldn't determine working directory: %v", dirErr)), nil
		}
		path = filepath.Join(workingDir, path)
	}

	// WriteBinaryFile, never WriteFile. On the remote path WriteFile carries
	// its content as a JSON string, which substitutes U+FFFD for every byte
	// that is not valid UTF-8 — it destroys a PNG at its signature byte and
	// returns no error at all.
	if _, err := tc.Daemon.WriteBinaryFile(tc.Context, path, att.Content); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Could not write attachment %s to %s: %v", attachmentID, path, err)), nil
	}

	logging.Info("Saved attachment to disk",
		"attachment_id", attachmentID,
		"path", path,
		"bytes", len(att.Content),
		"mime_type", att.MimeType,
	)

	output := SaveAttachmentOutput{
		AttachmentID: attachmentID,
		Filename:     att.Filename,
		MimeType:     att.MimeType,
		Size:         len(att.Content),
		SavedTo:      path,
	}
	return WithResponseMetadata(NewTextResponse(fmt.Sprintf(
		"Saved %s (%s, %d bytes) to %s", att.Filename, att.MimeType, len(att.Content), path)), output), nil
}

// callerUserID reads the caller from the request context, falling back to the
// chat's owner. The server executor puts the id on the context for every tool
// call; the chat lookup covers paths that construct a context by hand.
func (t *saveAttachmentTool) callerUserID(tc *rctx.ToolContext) string {
	if userID, ok := auth.GetUserIDFromContext(tc.Context); ok && userID != "" {
		return userID
	}
	if tc.ChatID == "" {
		return ""
	}
	chat, err := t.repo.GetChat(tc.Context, tc.ChatID)
	if err != nil || chat == nil {
		return ""
	}
	return chat.UserID
}
