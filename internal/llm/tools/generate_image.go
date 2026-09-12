// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/imagegen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// GenerateImageToolName is the registered name for the generate_image tool.
const GenerateImageToolName = ToolGenerateImage

// ImageGenerator is the one thing this tool needs from the driver layer:
// turn a prompt into bytes. Declared here, at the consumer, rather than
// exported from internal/llm/drivers/imagegen — which is both the package
// convention and, in this direction, a hard requirement: internal/llm/drivers
// already imports this package, so a direct import back would be a cycle.
type ImageGenerator interface {
	GenerateImage(ctx context.Context, request imagegen.Request) (*imagegen.Response, error)
}

// ImageGeneratorResolver produces a generator bound to whichever image model
// the selector resolves to for this user. It is a function rather than a
// pre-built generator because model selection depends on the calling user's
// credentials and tag preferences, which are only known per request.
//
// The implementation lives at the composition root (internal/serverapi,
// internal/serverworker), where drivers.ResolveImageGenerator is already in
// scope. A nil resolver is valid and means image generation is not available
// in this process — the daemon runtime builds a factory with no model registry
// at all, and every other tool must keep working there.
type ImageGeneratorResolver func(ctx context.Context, userID string, selector models.ModelSelector) (ImageGenerator, error)

// ImageGenTag is the tag an unconfigured image generation selects by.
//
// A strategy, not a model id. The tag re-resolves against the registry on
// every call, so a retired model drops out on its own; a pinned id keeps
// being requested until someone notices. Duplicated rather than imported from
// internal/llm/drivers, which already imports this package.
const ImageGenTag = "image-gen"

// GenerateImageParams describes one image to generate.
//
// `model` is a BOUND parameter, not an open one. It is declared here so a
// human can fix it — to a provider, a tag set, or a specific id — but it is
// bound by default (see DefaultBindings), so it never appears in the schema
// the agent sees and the agent can never name a model. That is the same
// outcome as the parameter not existing at all, which is what this was before,
// with the configurability added and nothing given away.
type GenerateImageParams struct {
	// Model selects which image model backs this call. Bound by default to
	// tags:[image-gen]; a human can rebind it to e.g.
	// {tags: [image-gen], providers: [codex]}.
	Model      models.ModelSelector `json:"model,omitempty" jsonschema:"description=Which image model to use. Bound by configuration; not settable per call."`
	Prompt     string               `json:"prompt" jsonschema:"required,description=What to generate. Describe the subject\\, style\\, composition and any text that must appear in the image. Longer and more specific prompts produce markedly better results than short ones."`
	Size       string               `json:"size,omitempty" jsonschema:"enum=1024x1024,enum=1536x1024,enum=1024x1536,enum=auto,description=Image dimensions. 1536x1024 is landscape\\, 1024x1536 is portrait. Defaults to the model's own default when omitted."`
	Quality    string               `json:"quality,omitempty" jsonschema:"enum=low,enum=medium,enum=high,enum=auto,description=Rendering effort. Higher quality costs more and takes longer. Defaults to the model's own default when omitted."`
	Background string               `json:"background,omitempty" jsonschema:"enum=transparent,enum=opaque,enum=auto,description=Use transparent for logos\\, icons and anything to be composited over other content. Ignored by models that do not support it."`
	SaveTo     string               `json:"save_to,omitempty" jsonschema:"description=Optional path to also write the image file to on disk. Relative paths resolve against the working directory. The image is stored and returned either way; this only additionally materializes it as a file."`
	Repo       string               `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo save_to is relative to: 'root' for the project root\\, or a repo name. Omit in single-repo projects or when save_to is absolute."`
}

const generateImageDescription = `Generate an image from a text description.

WHEN TO USE:
- The user asks for an image, illustration, diagram, icon, logo or mockup.
- You need a visual asset to write into the project (pair with save_to).

HOW TO USE:
- Describe the image in detail. Prompt quality dominates the result: name the
  subject, the style, the composition, the palette, and any text that must
  appear. A one-line prompt gets a one-line-prompt image.
- Use size to choose orientation, quality to trade cost for fidelity, and
  background=transparent for anything that will sit on top of other content.
- Set save_to to also write the file into the project. Without it the image is
  still generated and returned; it just is not written to disk.

WHAT YOU GET BACK:
- The image itself, which you can see and describe or critique.
- An attachment id. Cite that id when referring to this image later, and pass
  it to read_attachment to look at it again in a future turn.

NOTES:
- One image per call. Call again to iterate; say what to change rather than
  repeating the original prompt verbatim.
- You do not choose the model. It is resolved from the user's configured image
  model preference.`

// GenerateImageOutput is the structured result of one generation.
type GenerateImageOutput struct {
	AttachmentID string `json:"attachment_id"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int    `json:"size"`
	Model        string `json:"model"`
	// SavedTo is the resolved absolute path when save_to was requested and the
	// write succeeded. Empty otherwise.
	SavedTo string `json:"saved_to,omitempty"`
	// RevisedPrompt is the provider's rewritten prompt when it reports one.
	// Worth surfacing: it is the clearest signal of how the prompt was read.
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// generateImageTool generates an image, stores it as an attachment, and hands
// the bytes back to the model.
//
// It is a server-run tool: it needs the database and an outbound API call, and
// neither is available on the daemon. Writing the file to the user's machine
// still works from here — that goes through the daemon client on the tool
// context, which is exactly how every other server-run tool reaches disk.
type generateImageTool struct {
	repo    db.Repository
	resolve ImageGeneratorResolver
}

// NewGenerateImageTool creates the generate_image tool.
func NewGenerateImageTool(repo db.Repository, resolve ImageGeneratorResolver) Tool {
	return NewToolWrapper[GenerateImageParams, ToolResponse](&generateImageTool{repo: repo, resolve: resolve})
}

func (t *generateImageTool) Name() string {
	return GenerateImageToolName
}

func (t *generateImageTool) Description() string {
	return generateImageDescription
}

// DefaultBindings binds the model parameter so the agent never sees it, while
// leaving it configurable for a human.
//
// The default is a TAG, not a model id. Tags re-resolve against the registry
// on every call, so a retired model stops being selected the moment it leaves
// the registry; a pinned id keeps being requested until a human notices. We
// have shipped retired image models by pinning before.
func (t *generateImageTool) DefaultBindings() Bindings {
	return Bindings{
		"model": LiteralBinding(models.ModelSelector{Tags: []string{ImageGenTag}}),
	}
}

func (t *generateImageTool) RequiresPermission(params GenerateImageParams) (bool, error) {
	// Generating costs money and, with save_to, writes a file. Both are the
	// kind of side effect the permission layer exists for.
	return true, nil
}

func (t *generateImageTool) Execute(tc *rctx.ToolContext, params GenerateImageParams) (ToolResponse, error) {
	if strings.TrimSpace(params.Prompt) == "" {
		return NewTextErrorResponse("prompt is required"), nil
	}
	if t.repo == nil {
		return NewTextErrorResponse("image generation requires a database connection and is not available in this context"), nil
	}
	if t.resolve == nil {
		return NewTextErrorResponse("image generation is not available in this context"), nil
	}

	userID := t.userID(tc)
	if userID == "" {
		return NewTextErrorResponse("unable to determine user identity for image generation"), nil
	}

	generator, err := t.resolve(tc.Context, userID, params.Model)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Could not select an image model: %v", err)), nil
	}

	response, err := generator.GenerateImage(tc.Context, imagegen.Request{
		Prompt:     params.Prompt,
		Size:       params.Size,
		Quality:    params.Quality,
		Background: params.Background,
		Count:      1,
	})
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Image generation failed: %v", err)), nil
	}
	if response == nil || len(response.Images) == 0 {
		return NewTextErrorResponse("Image generation returned no images"), nil
	}

	image := response.Images[0]
	if len(image.Bytes) == 0 {
		return NewTextErrorResponse("Image generation returned an empty image"), nil
	}

	filename := generatedImageFilename(image.MIMEType)
	attachmentID, err := t.storeAttachment(tc.Context, userID, filename, image)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Generated the image but could not store it: %v", err)), nil
	}

	output := GenerateImageOutput{
		AttachmentID:  attachmentID,
		Filename:      filename,
		MimeType:      image.MIMEType,
		Size:          len(image.Bytes),
		Model:         response.ModelID,
		RevisedPrompt: image.RevisedPrompt,
	}

	var saveNote string
	if strings.TrimSpace(params.SaveTo) != "" {
		savedTo, saveErr := t.saveToDaemon(tc, params, image.Bytes)
		switch {
		case saveErr != nil:
			// The image exists and is addressable; only the file write
			// failed. Reporting that as a tool error would hide a usable
			// result, so it is a note on a successful response instead.
			saveNote = fmt.Sprintf("\n\nWARNING: could not write the file to %s: %v", params.SaveTo, saveErr)
		default:
			output.SavedTo = savedTo
			saveNote = fmt.Sprintf("\nSaved to: %s", savedTo)
		}
	}

	logging.Info("Generated image",
		"attachment_id", attachmentID,
		"model", response.ModelID,
		"driver", response.Driver,
		"bytes", len(image.Bytes),
		"mime_type", image.MIMEType,
		"litellm_call_id", response.LiteLLMCallID,
		"saved_to", output.SavedTo,
	)

	summary := fmt.Sprintf("Generated %s (%s, %d bytes) with %s.\nAttachment id: %s%s",
		filename, image.MIMEType, len(image.Bytes), response.ModelID, attachmentID, saveNote)
	if image.RevisedPrompt != "" {
		summary += fmt.Sprintf("\n\nThe model rewrote the prompt as: %s", image.RevisedPrompt)
	}

	return WithResponseMetadata(NewImageResponse(summary, []message.BinaryContent{{
		Path:     filename,
		MIMEType: image.MIMEType,
		Data:     image.Bytes,
	}}), output), nil
}

// userID reads the caller from the request context, falling back to the chat's
// owner. The server executor puts the id on the context for every tool call;
// the chat lookup covers paths that construct a context by hand.
func (t *generateImageTool) userID(tc *rctx.ToolContext) string {
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

// storeAttachment persists the bytes in the database. The database is the
// substrate, not the filesystem: in distributed mode neither the api server nor
// the worker can see the user's disk, so a file path would be unreadable by the
// process that has to serve /api/attachments/{id}.
func (t *generateImageTool) storeAttachment(ctx context.Context, userID, filename string, image imagegen.Image) (string, error) {
	attachmentID := uuid.New().String()
	hash := sha256.Sum256(image.Bytes)
	fileHash := hex.EncodeToString(hash[:])
	now := time.Now().UTC()

	att := &db.Attachment{
		ID:             attachmentID,
		UserID:         userID,
		Filename:       filename,
		Size:           int64(len(image.Bytes)),
		MimeType:       image.MIMEType,
		FileHash:       &fileHash,
		FilePath:       filepath.Join(userID, filename),
		AttachmentType: string(attachment.TypeImage),
		Content:        image.Bytes,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := t.repo.CreateAttachment(ctx, att); err != nil {
		return "", err
	}
	return attachmentID, nil
}

// saveToDaemon writes the image to the user's filesystem through the daemon
// client on the tool context. WriteBinaryFile, never WriteFile: on the remote
// path WriteFile carries its content as a JSON string, which substitutes
// U+FFFD for every byte that is not valid UTF-8 and destroys a PNG at its
// signature, with no error returned.
func (t *generateImageTool) saveToDaemon(tc *rctx.ToolContext, params GenerateImageParams, content []byte) (string, error) {
	if tc.Daemon == nil {
		return "", fmt.Errorf("writing a file requires a connected daemon")
	}

	path := params.SaveTo
	if !filepath.IsAbs(path) {
		workingDir, err := ResolveRepoPath(tc, params.Repo)
		if err != nil {
			return "", fmt.Errorf("couldn't determine working directory: %w", err)
		}
		path = filepath.Join(workingDir, path)
	}

	if _, err := tc.Daemon.WriteBinaryFile(tc.Context, path, content); err != nil {
		return "", err
	}
	return path, nil
}

// generatedImageFilename names the file after the MIME type the bytes actually
// carry, which imagegen sniffs from the bytes themselves rather than trusting
// the requested format. The extension has to agree with the content because
// attachment.GetAttachmentType classifies by extension, and an attachment that
// is not classified as an image never becomes an image block.
func generatedImageFilename(mimeType string) string {
	extension := ".png"
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		extension = ".jpg"
	case "image/webp":
		extension = ".webp"
	case "image/gif":
		extension = ".gif"
	}
	return "generated-" + uuid.New().String()[:8] + extension
}
