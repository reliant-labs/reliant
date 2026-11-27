// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // register the GIF decoder for image.DecodeConfig
	_ "image/jpeg" // register the JPEG decoder for image.DecodeConfig
	_ "image/png"  // register the PNG decoder for image.DecodeConfig
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.DecodeConfig

	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// PresentImageToolName is the registered name for the present_image tool.
const PresentImageToolName = ToolPresentImage

const (
	// maxPresentImageBytes is the largest file present_image shows. The image
	// is for a person, not a model, so the model's 3.75MB ceiling does not
	// apply; this one bounds what one call moves through the daemon (a reply
	// is base64 over NATS, capped at 64MB) and stores as an attachment row.
	maxPresentImageBytes = 20 * 1024 * 1024

	// maxPresentImages is the most files one call shows. A set reads as a
	// set; past this it is a gallery, and the transcript is the wrong place.
	maxPresentImages = 10
)

// presentImageMIMETypes are the image types every browser the app runs in
// renders from an <img>. SVG is deliberately absent: it can carry script, and
// attachments are served from the app's own origin.
var presentImageMIMETypes = map[string]string{
	"image/png":  "PNG",
	"image/jpeg": "JPEG",
	"image/gif":  "GIF",
	"image/webp": "WebP",
}

// PresentImageParams names the files to show the user.
//
// repo is spelled and described as on view: paths here are files on the
// machine, read the same way view reads them.
type PresentImageParams struct {
	Paths   []string `json:"paths" jsonschema:"required,description=Image files to show the user (PNG\\, JPEG\\, GIF or WebP). Relative paths resolve against the working directory. Up to 10 per call."`
	Caption string   `json:"caption,omitempty" jsonschema:"description=One line shown under the image(s) saying what the user is looking at."`
	Repo    string   `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo the paths are relative to: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Omit in single-repo projects or when the paths are absolute."`
}

const presentImageDescription = `Show an image file from the machine to the user, in the chat.

WHEN TO USE:
- You made or found an image the USER should look at: a screenshot you took,
  a chart or diagram you rendered, a generated asset, a visual diff.
- The user may be on another device than the machine (a cloud machine, a
  remote box), so a path like /tmp/shot.png is something they cannot open.
  This puts the picture itself in the conversation, at a size they can read,
  with click-to-enlarge.

THIS IS NOT view:
- view hands an image to YOU, so you can look at it. This hands it to the
  USER. Presenting does not show you the image; if you need to see it too,
  view the same file.

HOW TO USE:
- Pass one or more paths; relative paths resolve against the working
  directory, and repo picks the base in a multi-repo project.
- Add a caption when the image needs one line of context ("Login page after
  the fix", "p95 latency, last 24h").

NOTES:
- PNG, JPEG, GIF and WebP, up to 20MB each and 10 per call. Render SVG to PNG
  first (e.g. ` + "`rsvg-convert -o out.png in.svg`" + `).
- Each image is stored as an attachment owned by the user; the result reports
  its attachment id, which save_attachment can write elsewhere later.`

// PresentImageOutput is the structured result of one call. attachment_ids is
// the field execute_tools reads to put the images in the timeline.
type PresentImageOutput struct {
	AttachmentIDs []string           `json:"attachment_ids"`
	Images        []PresentedImage   `json:"images"`
	Skipped       []PresentImageSkip `json:"skipped,omitempty"`
	Caption       string             `json:"caption,omitempty"`
}

// PresentedImage is one image the user can now see.
type PresentedImage struct {
	AttachmentID string `json:"attachment_id"`
	// Path is the resolved ABSOLUTE path read, for the same reason
	// save_attachment echoes one: the model's path is usually relative.
	Path     string `json:"path"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Size     int    `json:"size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// PresentImageSkip is a path that was not shown, and why.
type PresentImageSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// presentImageTool reads image files from the machine and stores them as
// user-only attachments, which the timeline renders after the tool call.
//
// Server-run: the attachment row is in the database, which the daemon cannot
// reach. The bytes still come from the user's machine, through the daemon
// client on the tool context — the reverse of save_attachment's route.
//
// The rows are attachment.TypeDisplay, not TypeImage: the history replay
// never hands them to a model (messageconv.userOnlyMedia), so an image is not
// bound by a model's size limits and does not cost tokens on every later turn.
type presentImageTool struct {
	repo presentImageStore
}

// NewPresentImageTool creates the present_image tool.
func NewPresentImageTool(repo presentImageStore) Tool {
	return NewToolWrapper[PresentImageParams, ToolResponse](&presentImageTool{repo: repo})
}

func (t *presentImageTool) Name() string {
	return PresentImageToolName
}

func (t *presentImageTool) Description() string {
	return presentImageDescription
}

// IsReadOnly: it reads files and changes nothing on the machine. The row it
// writes is the user's own, shown only to them.
func (t *presentImageTool) IsReadOnly() bool {
	return true
}

func (t *presentImageTool) RequiresPermission(params PresentImageParams) (bool, error) {
	// The same read view does without asking, shown to the same user who
	// could ask view's result to be shown.
	return false, nil
}

func (t *presentImageTool) Execute(tc *rctx.ToolContext, params PresentImageParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("presenting an image requires a database connection and is not available in this context"), nil
	}
	paths := make([]string, 0, len(params.Paths))
	for _, p := range params.Paths {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return NewTextErrorResponse("paths is required: name at least one image file to show"), nil
	}
	if len(paths) > maxPresentImages {
		return NewTextErrorResponse(fmt.Sprintf(
			"%d paths given; present_image shows at most %d per call. Pick the ones that matter, or call again.",
			len(paths), maxPresentImages)), nil
	}
	if tc.Daemon == nil {
		return NewTextErrorResponse("Reading a file from the machine requires a connected daemon, and none is available in this context."), nil
	}
	userID, orgID := t.owner(tc)
	if userID == "" {
		return NewTextErrorResponse("unable to determine the user to show the image to"), nil
	}

	// The working directory is needed only for a relative path, so a call
	// with absolute paths works where none resolves; display paths fall
	// back to the absolute one.
	workingDir, dirErr := ResolveRepoPath(tc, params.Repo)

	caption := strings.TrimSpace(params.Caption)
	output := PresentImageOutput{Caption: caption}
	for _, raw := range paths {
		path := raw
		if !filepath.IsAbs(path) {
			if dirErr != nil {
				output.Skipped = append(output.Skipped, PresentImageSkip{Path: raw,
					Reason: fmt.Sprintf("couldn't determine the working directory (%v); pass an absolute path", dirErr)})
				continue
			}
			path = filepath.Join(workingDir, path)
		}
		display := raw
		if dirErr == nil {
			if rel, err := filepath.Rel(workingDir, path); err == nil && !strings.HasPrefix(rel, "..") {
				display = rel
			}
		}

		presented, reason := t.present(tc, userID, orgID, path)
		if reason != "" {
			output.Skipped = append(output.Skipped, PresentImageSkip{Path: display, Reason: reason})
			continue
		}
		output.Images = append(output.Images, presented)
		output.AttachmentIDs = append(output.AttachmentIDs, presented.AttachmentID)
	}

	if len(output.Images) == 0 {
		lines := make([]string, 0, len(output.Skipped))
		for _, skip := range output.Skipped {
			lines = append(lines, fmt.Sprintf("- %s: %s", skip.Path, skip.Reason))
		}
		return NewTextErrorResponse("Nothing was shown to the user:\n" + strings.Join(lines, "\n")), nil
	}

	logging.Debug("Presented images to the user",
		"chat_id", tc.ChatID,
		"attachment_ids", output.AttachmentIDs,
		"skipped", len(output.Skipped),
	)
	return WithResponseMetadata(NewTextResponse(presentImageSummary(output)), output), nil
}

// present reads one file and stores it, returning the image or the reason it
// was not shown.
func (t *presentImageTool) present(tc *rctx.ToolContext, userID string, orgID *string, path string) (PresentedImage, string) {
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		return PresentedImage{}, "SVG is not shown (it can carry script); render it to PNG first, e.g. `rsvg-convert -o out.png in.svg`"
	}
	stat, err := tc.Daemon.StatFile(tc.Context, path)
	if err != nil {
		return PresentedImage{}, fmt.Sprintf("could not access the file: %v", err)
	}
	if !stat.Exists {
		return PresentedImage{}, "file not found"
	}
	if stat.IsDir {
		return PresentedImage{}, "is a directory, not a file"
	}
	if stat.Size > maxPresentImageBytes {
		return PresentedImage{}, fmt.Sprintf("is %s, above the %s present_image shows; downscale it first (e.g. `convert in.png -resize 50%% out.png`)",
			formatFileSize(stat.Size), formatFileSize(maxPresentImageBytes))
	}
	if stat.Size == 0 {
		return PresentedImage{}, "the file is empty"
	}

	// ReadBinaryFile, never ReadFile: the text read path decodes content as
	// UTF-8, which corrupts an image at its first non-UTF-8 byte.
	data, err := tc.Daemon.ReadBinaryFile(tc.Context, path, maxPresentImageBytes)
	if err != nil {
		return PresentedImage{}, fmt.Sprintf("could not read the file: %v", err)
	}

	// The type is sniffed from the bytes, never taken from the extension: a
	// PNG named .jpg is still a PNG, and a text file named .png is not an
	// image at all.
	mimeType := http.DetectContentType(data)
	kind, ok := presentImageMIMETypes[mimeType]
	if !ok {
		return PresentedImage{}, fmt.Sprintf("is not a PNG, JPEG, GIF or WebP image (its content reads as %s)", mimeType)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return PresentedImage{}, fmt.Sprintf("is not a readable %s (%v)", kind, err)
	}

	filename := filepath.Base(path)
	id, err := t.store(tc.Context, userID, orgID, filename, mimeType, data)
	if err != nil {
		return PresentedImage{}, fmt.Sprintf("could not be stored: %v", err)
	}
	return PresentedImage{
		AttachmentID: id,
		Path:         path,
		Filename:     filename,
		MimeType:     mimeType,
		Size:         len(data),
		Width:        cfg.Width,
		Height:       cfg.Height,
	}, ""
}

// store writes the image as a user-only attachment row. The database is the
// substrate: neither the api server nor the worker can see the machine's
// disk, and /api/attachments/{id} is served from the row.
func (t *presentImageTool) store(ctx context.Context, userID string, orgID *string, filename, mimeType string, data []byte) (string, error) {
	id := uuid.New().String()
	hash := sha256.Sum256(data)
	fileHash := hex.EncodeToString(hash[:])
	now := time.Now().UTC()
	att := &db.Attachment{
		ID:             id,
		UserID:         userID,
		Filename:       filename,
		Size:           int64(len(data)),
		MimeType:       mimeType,
		FileHash:       &fileHash,
		FilePath:       filepath.Join(userID, id, filename),
		AttachmentType: string(attachment.TypeDisplay),
		Content:        data,
		OrgID:          orgID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := t.repo.CreateAttachment(ctx, att); err != nil {
		return "", err
	}
	return id, nil
}

// presentImageSummary is what the model reads. It says plainly that the user
// was shown the image and the model was not, because a model that believes it
// has seen a picture describes one it never looked at.
func presentImageSummary(output PresentImageOutput) string {
	var b strings.Builder
	noun := "image"
	if len(output.Images) > 1 {
		noun = "images"
	}
	fmt.Fprintf(&b, "Showed %d %s to the user in the chat:\n", len(output.Images), noun)
	for _, img := range output.Images {
		fmt.Fprintf(&b, "- %s (%s, %dx%d, %s), attachment id %s\n",
			img.Filename, img.MimeType, img.Width, img.Height, formatFileSize(int64(img.Size)), img.AttachmentID)
	}
	if len(output.Skipped) > 0 {
		b.WriteString("\nNot shown:\n")
		for _, skip := range output.Skipped {
			fmt.Fprintf(&b, "- %s: %s\n", skip.Path, skip.Reason)
		}
	}
	b.WriteString("\nThe user can see it now; you cannot. To look at an image yourself, view its file. " +
		"To copy one elsewhere, use save_attachment with its attachment id.")
	return b.String()
}

// owner resolves the owner of the chat receiving the attachment reference,
// and the org stamped on the attachment (the chat's, or nil for none).
// Never substitute the request-context identity: it may belong to another
// user, and a missing chat or failed lookup must not create an attachment.
func (t *presentImageTool) owner(tc *rctx.ToolContext) (string, *string) {
	if tc.ChatID == "" {
		return "", nil
	}
	chat, err := t.repo.GetChat(tc.Context, tc.ChatID)
	if err != nil || chat == nil {
		return "", nil
	}
	return chat.UserID, orgOf(chat)
}
