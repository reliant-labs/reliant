// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // register the GIF decoder for image.DecodeConfig
	_ "image/jpeg" // register the JPEG decoder for image.DecodeConfig
	"image/png"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/image/bmp"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.DecodeConfig

	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const (
	// maxModelImageBytes is the largest image a tool result hands a model.
	// Anthropic refuses an image whose base64 exceeds 5MiB, and base64 is 4/3
	// of the raw size. A refused image is not one failed call: the image is
	// durable history, so every later request of the chat would be refused
	// with it.
	maxModelImageBytes = 5 * 1024 * 1024 * 3 / 4

	// maxModelImageDimension is the largest width or height a provider takes
	// (Anthropic: 8000px). A taller full-page screenshot is refused outright.
	maxModelImageDimension = 8000
)

// modelImageMIMETypes are the image types every vision-capable provider we
// route accepts. BMP is converted to PNG on the way in; SVG and anything else
// is refused with a note rather than sent to a provider that would reject the
// request.
var modelImageMIMETypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// persistToolMedia makes a tool result's images and PDFs durable, so the model
// actually receives them.
//
// A tool hands the model media as ToolResult.BinaryParts (the view tool's
// screenshot, an MCP tool's image, read_attachment's PDF pages). Those bytes
// live only in this activity's memory: the result crosses the workflow
// boundary as a ToolResultMsg, which carries attachment ids and no bytes, and
// the next CallLLM rebuilds the prompt from the database. So until this ran,
// every such image was dropped between the tool and the model, and the model
// saw only the tool's caption, e.g. "Image file: shot.png (image/png, 241.0KB)".
//
// Each part becomes an attachment row and its id joins the result's
// attachment ids — the road generate_image already takes. SaveMessage writes
// one IMAGE/DOCUMENT block per id beside the tool_result block, the history
// reload folds them back into the result's BinaryParts
// (messageconv.foldToolResultMedia), and each driver encodes them for its
// provider.
//
// The attachment ids a tool reports in its metadata (generate_image's own
// row, save_attachment's source) are not trusted: each joins the result only
// if it exists and the chat's user owns it, so a tool cannot attach — and the
// replay cannot hand the model — another user's attachment. A part is not
// stored twice only when an attachment this result carries already replays
// to the model as the same bytes: an owned image or document the tool
// reported, or a part stored earlier in this result. Every other part is
// persisted, whatever ids the tool reported.
//
// A part a provider would refuse — too large, too many pixels, a type no
// provider takes — is not persisted, and the result's text says why, so the
// model can act on it (resize, crop, read SVG as text) instead of retrying
// blind. It returns the result's content and its attachment ids.
func (a *ExecuteToolsActivity) persistToolMedia(ctx context.Context, tec *toolExecutionContext, content string, parts []message.BinaryContent, attachmentIDs []string) (string, []string) {
	if len(parts) == 0 && len(attachmentIDs) == 0 {
		return content, nil
	}
	userID := ""
	if tec.project != nil {
		userID = tec.project.UserID
	}

	ids := make([]string, 0, len(attachmentIDs)+len(parts))
	// shown maps a content hash to the id of an attachment in ids that the
	// replay hands the model as those exact bytes. A part is skipped only when
	// it is found here, so the attachment standing for it is always one this
	// result carries. Parts stored below join it, so a repeated part is stored
	// once.
	shown := make(map[string]string, len(attachmentIDs)+len(parts))
	for _, id := range attachmentIDs {
		att, err := a.repo.GetAttachment(ctx, id)
		if err != nil || att == nil || !authz.CanAccessPersonal(userID, att.UserID) {
			logging.WarnContext(ctx, "[ExecuteTools] Dropping a tool-reported attachment id the chat's user does not own",
				"chat_id", tec.getChatID(), "tool", tec.toolName, "tool_call_id", tec.toolCallID,
				"attachment_id", id, "error", err)
			continue
		}
		ids = append(ids, id)
		// Only an image or a document replays to the model as its bytes
		// (threads.buildToolAttachmentBlock, messageconv.foldToolResultMedia);
		// a video or a reference with the same bytes does not stand for a part.
		if replaysToModel(att.AttachmentType) && len(att.Content) > 0 {
			hash := contentHash(att.Content)
			if _, ok := shown[hash]; !ok {
				shown[hash] = id
			}
		}
	}

	var notes []string
	for i, part := range parts {
		if _, ok := shown[contentHash(part.Data)]; ok {
			continue
		}
		normalized, attType, reason := normalizeToolMedia(part)
		if reason != "" {
			notes = append(notes, fmt.Sprintf("%s is not shown: %s", toolMediaName(part, i), reason))
			continue
		}
		// A BMP is compared again as the PNG it became.
		hash := contentHash(normalized.Data)
		if _, ok := shown[hash]; ok {
			continue
		}
		id, err := a.storeToolMedia(ctx, userID, tec.toolName, i, normalized, attType)
		if err != nil {
			logging.ErrorContext(ctx, "[ExecuteTools] Failed to persist tool result media; the model will not see it",
				"chat_id", tec.getChatID(), "tool", tec.toolName, "tool_call_id", tec.toolCallID,
				"mime_type", part.MIMEType, "bytes", len(part.Data), "error", err)
			notes = append(notes, fmt.Sprintf("%s is not shown: it could not be saved", toolMediaName(part, i)))
			continue
		}
		ids = append(ids, id)
		shown[hash] = id
		shown[contentHash(part.Data)] = id
	}
	if len(notes) > 0 {
		content += "\n\n" + strings.Join(notes, "\n")
	}
	if len(ids) == 0 {
		return content, nil
	}
	return content, ids
}

// normalizeToolMedia returns the part as a model may receive it and its
// attachment type, or the reason it may not.
func normalizeToolMedia(part message.BinaryContent) (message.BinaryContent, attachment.AttachmentType, string) {
	if len(part.Data) == 0 {
		return part, "", "it is empty"
	}
	mimeType := strings.ToLower(part.MIMEType)
	if mimeType == "application/pdf" {
		return part, attachment.TypeDocument, ""
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return part, "", fmt.Sprintf("%s is not a type a model can read", part.MIMEType)
	}

	switch mimeType {
	case "image/bmp", "image/x-bmp", "image/x-ms-bmp":
		converted, err := bmpToPNG(part.Data)
		if err != nil {
			return part, "", fmt.Sprintf("it is not a readable BMP (%v)", err)
		}
		part.Data = converted
		part.MIMEType = "image/png"
		if part.Path != "" {
			part.Path = strings.TrimSuffix(part.Path, filepath.Ext(part.Path)) + ".png"
		}
		mimeType = "image/png"
	case "image/svg+xml":
		return part, "", "SVG is not an image type models accept; read the file as text instead"
	}
	if !modelImageMIMETypes[mimeType] {
		return part, "", fmt.Sprintf("%s is not an image type models accept (PNG, JPEG, GIF and WebP are)", part.MIMEType)
	}
	part.MIMEType = mimeType

	if len(part.Data) > maxModelImageBytes {
		return part, "", fmt.Sprintf("it is %s, above the %s a model accepts; downscale or crop it first",
			humanBytes(len(part.Data)), humanBytes(maxModelImageBytes))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(part.Data))
	if err != nil {
		return part, "", fmt.Sprintf("it is not a readable %s (%v)", part.MIMEType, err)
	}
	if cfg.Width > maxModelImageDimension || cfg.Height > maxModelImageDimension {
		return part, "", fmt.Sprintf("it is %dx%d px, above the %dpx a model accepts on either side; downscale or crop it first",
			cfg.Width, cfg.Height, maxModelImageDimension)
	}
	return part, attachment.TypeImage, ""
}

// replaysToModel reports whether an attachment of this type reaches the model
// as its bytes when it rides on a tool result.
func replaysToModel(attType string) bool {
	return attType == string(attachment.TypeImage) || attType == string(attachment.TypeDocument)
}

func contentHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func bmpToPNG(data []byte) ([]byte, error) {
	img, err := bmp.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// storeToolMedia writes one part as an attachment row, the substrate every
// process can read: neither the api server nor the worker can see the
// machine's disk the bytes came from.
func (a *ExecuteToolsActivity) storeToolMedia(ctx context.Context, userID, toolName string, index int, part message.BinaryContent, attType attachment.AttachmentType) (string, error) {
	id := uuid.New().String()
	fileHash := contentHash(part.Data)
	filename := toolMediaFilename(part, toolName, index)
	now := time.Now().UTC()
	att := &db.Attachment{
		ID:             id,
		UserID:         userID,
		Filename:       filename,
		Size:           int64(len(part.Data)),
		MimeType:       part.MIMEType,
		FileHash:       &fileHash,
		FilePath:       filepath.Join(userID, id, filename),
		AttachmentType: string(attType),
		Content:        part.Data,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := a.repo.CreateAttachment(ctx, att); err != nil {
		return "", err
	}
	return id, nil
}

// toolMediaFilename names the attachment after the file the tool read, or
// after the tool when the bytes came from no file (an MCP screenshot).
func toolMediaFilename(part message.BinaryContent, toolName string, index int) string {
	if base := filepath.Base(part.Path); part.Path != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	ext := ".bin"
	switch part.MIMEType {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	case "application/pdf":
		ext = ".pdf"
	}
	if toolName == "" {
		toolName = "tool"
	}
	return fmt.Sprintf("%s-%d%s", toolName, index+1, ext)
}

// toolMediaName is how a note refers to a part: its file, or its position.
func toolMediaName(part message.BinaryContent, index int) string {
	if part.Path != "" {
		return filepath.Base(part.Path)
	}
	return fmt.Sprintf("Attachment %d (%s)", index+1, part.MIMEType)
}

func humanBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
