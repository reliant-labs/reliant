// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/videogen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/videojobs"
)

// GenerateVideoToolName is the registered name for the generate_video tool.
const GenerateVideoToolName = ToolGenerateVideo

// VideoGenerator is the video client this tool needs from the driver layer.
// Declared here, at the consumer, for the same cycle reason as ImageGenerator:
// internal/llm/drivers already imports this package.
//
// Submit and Poll are separate on purpose: the tool records the provider job
// between them, which is what lets a re-dispatched activity resume a render
// instead of paying for a second one.
type VideoGenerator interface {
	Info() videogen.ModelInfo
	Submit(ctx context.Context, request videogen.Request) (videogen.Job, error)
	Poll(ctx context.Context, job videogen.Job) (*videogen.Response, bool, error)
	Cancel(ctx context.Context, job videogen.Job) error
}

// VideoGeneratorResolver produces a generator bound to whichever video model
// the selector resolves to for this user. Nil means video generation is not
// available in this process (the daemon runtime).
type VideoGeneratorResolver func(ctx context.Context, userID string, selector models.ModelSelector) (VideoGenerator, error)

// VideoJobStore is the durable record of provider jobs, keyed by tool call id.
type VideoJobStore interface {
	GetByToolCall(ctx context.Context, toolCallID string) (*videojobs.Job, error)
	GetByAttachment(ctx context.Context, attachmentID string) (*videojobs.Job, error)
	Create(ctx context.Context, job *videojobs.Job) error
	Complete(ctx context.Context, toolCallID, attachmentID string) error
	Finish(ctx context.Context, toolCallID string, state videojobs.State, message string) error
}

// VideoGenTag is the base tag for video-generation models. Duplicated rather
// than imported from internal/llm/drivers, which already imports this package.
const VideoGenTag = "video-gen"

// GenerateVideoParams describes one video to generate. `model` and `quality`
// are both OPEN: the agent chooses by intent (quality) or names a model. A
// workflow author can still bind either one to lock it, which removes it from
// the schema the agent sees.
type GenerateVideoParams struct {
	Model           models.ModelSelector `json:"model,omitempty" jsonschema:"description=Optional exact video model id."`
	Quality         string               `json:"quality,omitempty" jsonschema:"enum=fast,enum=standard,enum=cinematic,description=Which tier of video model to use. standard (default)\\, fast or cinematic. See the tool description for when to pick each."`
	Prompt          string               `json:"prompt" jsonschema:"required,description=What to generate. Name the subject\\, the action\\, the camera and the style\\, and any sound or dialogue wanted. Describe ONE continuous shot unless you want cuts."`
	DurationSeconds int                  `json:"duration_seconds,omitempty" jsonschema:"description=Clip length in seconds. Veo allows 4\\, 6 or 8; Omni allows 3 to 10. Cost scales with length. Defaults to the model's own default."`
	Resolution      string               `json:"resolution,omitempty" jsonschema:"enum=360p,enum=720p,enum=1080p,enum=4k,description=Output resolution. Defaults to 720p because cost rises with resolution. Veo needs duration 8 for 1080p and 4k."`
	AspectRatio     string               `json:"aspect_ratio,omitempty" jsonschema:"enum=16:9,enum=9:16,description=16:9 is landscape and the default; 9:16 is portrait."`
	StartFrame      string               `json:"start_frame,omitempty" jsonschema:"description=Attachment id of an image to animate as the first frame."`
	EndFrame        string               `json:"end_frame,omitempty" jsonschema:"description=Attachment id of an image the clip should end on. Requires start_frame."`
	ReferenceImages []string             `json:"reference_images,omitempty" jsonschema:"description=Up to 3 attachment ids of images whose subject or style should appear in the clip."`
	EditFrom        string               `json:"edit_from,omitempty" jsonschema:"description=Edit handle of a video you generated earlier. The prompt then describes only the CHANGE\\, for example 'make it slower and keep everything else the same'."`
	NegativePrompt  string               `json:"negative_prompt,omitempty" jsonschema:"description=Things to keep out of the clip. Only some models support it; others return an error telling you to say it in the prompt."`
	Audio           *bool                `json:"audio,omitempty" jsonschema:"description=Set false to request a silent clip. Not every provider can honor it."`
	SaveTo          string               `json:"save_to,omitempty" jsonschema:"description=Optional path to also write the video file to on disk. Relative paths resolve against the working directory. The video is stored and returned either way."`
	Repo            string               `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo save_to is relative to: 'root' for the project root\\, or a repo name."`
}

// JSONSchemaExtend lists the valid model ids on the open `model` parameter.
func (GenerateVideoParams) JSONSchemaExtend(s *jsonschema.Schema) { videoKind.extendMediaSchema(s) }

const generateVideoDescription = `Generate a short video clip from a text description.

CHOOSING A MODEL (set quality; omit it for standard):
- standard (default): the everyday choice. A multimodal generalist: best for
  iterating, editing a previous clip (edit_from), keeping characters consistent
  with reference images, and physically plausible motion. 3–10s. It RENDERS at
  720p; 1080p and 4K are upscaled from that, so they are not truly sharper.
- cinematic: a polished one-shot final. A dedicated cinematic engine that
  renders 1080p and 4K NATIVELY, with native audio and the best clean
  first-pass quality. Pick it when sharpness or a finished look matters. Costs
  about 4x more than standard, and cannot be edited conversationally (it can
  only be extended), so each change is a full re-render.
- fast: drafts and previews. The cheapest tier; no 4K, no reference images.
- To pin one exact model instead, pass model with an id.
- Ask the user first when the trade-off matters, for example cost when they
  want 4K or a long cinematic clip.
- The result names the model that actually ran and why. Tell the user, and
  change quality if it was not what they wanted.

COST AND TIME:
- Costs roughly $0.05 to $0.60 PER SECOND of video, depending on the model and
  resolution. Resolution defaults to 720p to keep cost down. Prefer short clips.
- A render takes 30 seconds to several minutes and blocks this turn until it
  finishes.

HOW TO USE:
- Describe the subject, the action, the camera and the style. Mention sound or
  dialogue if you want it.
- BY DEFAULT THE MODEL CUTS BETWEEN SEVERAL SHOTS and invents a small narrative.
  Unless the user wants cuts, write "in a single continuous shot, no scene cuts"
  (or "single unbroken scene") into the prompt. Without it a request for one
  moment comes back as a montage.
- To animate an existing image set start_frame to its attachment id.
- To refine a clip you already made, pass its edit handle as edit_from and
  describe ONLY the change ("make it slower, keep everything else the same").
  Keep edit prompts short: long, descriptive ones change things you did not ask
  about. Only models that support edits give you an edit handle; otherwise
  regenerate with the full prompt.

WHAT YOU GET BACK:
- Text only. YOU CANNOT SEE THE VIDEO. The user can play it in the chat. Say
  what you asked for, not what the video shows, and invite them to review it.
- An attachment id, and an edit handle when the model supports edits.

NOTES:
- A model or quality the user's providers cannot serve returns an error naming
  the tiers that are available.`

// GenerateVideoOutput is the structured result of one generation. It carries
// ids and numbers, never bytes: this value lands in Temporal history and logs.
type GenerateVideoOutput struct {
	AttachmentID    string `json:"attachment_id"`
	Filename        string `json:"filename"`
	MimeType        string `json:"mime_type"`
	Size            int    `json:"size"`
	Model           string `json:"model"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	AspectRatio     string `json:"aspect_ratio,omitempty"`
	Prompt          string `json:"prompt"`
	EditHandle      string `json:"edit_handle,omitempty"`
	ProviderJob     string `json:"provider_job,omitempty"`
	// Chosen is why this model ran, e.g. "quality=cinematic".
	Chosen         string `json:"chosen,omitempty"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
	SavedTo        string `json:"saved_to,omitempty"`
}

type generateVideoTool struct {
	repo    db.Repository
	jobs    VideoJobStore
	resolve VideoGeneratorResolver
	wait    videogen.WaitOptions
}

// NewGenerateVideoTool creates the generate_video tool.
func NewGenerateVideoTool(repo db.Repository, jobs VideoJobStore, resolve VideoGeneratorResolver) Tool {
	return NewToolWrapper[GenerateVideoParams, ToolResponse](&generateVideoTool{repo: repo, jobs: jobs, resolve: resolve})
}

func (t *generateVideoTool) Name() string        { return GenerateVideoToolName }
func (t *generateVideoTool) Description() string { return generateVideoDescription }

// RequiresPermission is always true: generation costs real money per second.
func (t *generateVideoTool) RequiresPermission(params GenerateVideoParams) (bool, error) {
	return true, nil
}

func (t *generateVideoTool) Execute(tc *rctx.ToolContext, params GenerateVideoParams) (ToolResponse, error) {
	if strings.TrimSpace(params.Prompt) == "" {
		return NewTextErrorResponse("prompt is required"), nil
	}
	if t.repo == nil || t.jobs == nil {
		return NewTextErrorResponse("video generation requires a database connection and is not available in this context"), nil
	}
	if t.resolve == nil {
		return NewTextErrorResponse("video generation is not available in this context"), nil
	}
	userID := t.userID(tc)
	if userID == "" {
		return NewTextErrorResponse("unable to determine user identity for video generation"), nil
	}
	toolCallID := currentToolCallID(tc)

	// A job already recorded under this tool call id means this is a
	// re-dispatch (the worker died mid-render). Resume it: Poll, never Submit.
	if toolCallID != "" {
		prior, err := t.jobs.GetByToolCall(tc.Context, toolCallID)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Could not check for an earlier render of this call: %v", err)), nil
		}
		if prior != nil {
			return t.resume(tc, userID, prior, params)
		}
	}

	choice, err := chooseMedia(videoKind, params.Model, params.Quality)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	var edit *videojobs.Job
	if params.EditFrom != "" {
		var errResp *ToolResponse
		edit, errResp = t.lookupEdit(tc.Context, userID, params.EditFrom)
		if errResp != nil {
			return *errResp, nil
		}
		// Edit state lives with one provider session: same model, same driver.
		choice = mediaChoice{
			Selector: models.ModelSelector{ID: edit.ModelID, Providers: []string{edit.Driver}},
			Reason:   fmt.Sprintf("edit_from (continues %s)", edit.ModelID),
		}
	}

	generator, err := t.resolve(tc.Context, userID, choice.Selector)
	if err != nil {
		var unavailable *models.MediaUnavailableError
		if !errors.As(err, &unavailable) {
			if msg := t.tierUnavailable(tc, userID, choice, ""); msg != "" {
				return NewTextErrorResponse(msg), nil
			}
		}
		return NewTextErrorResponse(mediaResolveFailure("Could not select a video model", err)), nil
	}
	info := generator.Info()
	if msg := t.tierUnavailable(tc, userID, choice, info.ModelID); msg != "" {
		return NewTextErrorResponse(msg), nil
	}

	request, errMsg := t.buildRequest(tc.Context, userID, params, edit, info)
	if errMsg != "" {
		return NewTextErrorResponse(errMsg), nil
	}

	submitted, err := generator.Submit(tc.Context, request)
	if err != nil {
		return NewTextErrorResponse(videoErrorMessage("Video generation could not be started", err)), nil
	}
	job := &videojobs.Job{
		ToolCallID: toolCallID, UserID: userID, ChatID: tc.ChatID,
		Driver: info.Driver, ModelID: info.ModelID, APIModel: info.APIModel,
		ProviderJob: submitted.ID, State: videojobs.StateSubmitted,
	}
	if toolCallID != "" {
		if err := t.jobs.Create(tc.Context, job); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Video generation started (provider job %s) but the job could not be recorded: %v", submitted.ID, err)), nil
		}
	}
	started := time.Now()

	chosen := chosenLine(info.ModelID, choice.Reason)
	chosen += t.substitutionNote(tc, userID, choice, info.ModelID)
	if info.Capabilities != nil && info.Capabilities.IsUpscaled(params.Resolution) {
		chosen += fmt.Sprintf(" Note: %s renders at %s natively; %s is upscaled. For sharp native %s use quality=cinematic.",
			info.ModelID, strings.Join(info.Capabilities.NativeResolutions, "/"), params.Resolution, params.Resolution)
	}
	return t.await(tc, generator, job, submitted, params, request, started, chosen)
}

// substitutionNote makes a tier substitution visible: when the tier's catalog
// top pick is not servable with the user's providers and a different model
// ran, it says which model was skipped and what unlocks it.
func (t *generateVideoTool) substitutionNote(tc *rctx.ToolContext, userID string, choice mediaChoice, usedModel string) string {
	if choice.Tier == "" || len(choice.Selector.Tags) < 2 {
		return ""
	}
	registry, err := models.GetRegistry()
	if err != nil {
		return ""
	}
	for _, def := range registry.GetModelsByTag(choice.Selector.Tags[1]) {
		if def == nil || !def.Capabilities.CanOutput(models.ModalityVideo) {
			continue
		}
		if def.ID == usedModel {
			return ""
		}
		if _, err := t.resolve(tc.Context, userID, models.ModelSelector{ID: def.ID}); err == nil {
			return ""
		}
		need := "a provider you have not connected"
		if len(def.Providers) > 0 {
			need = models.MediaProviderLabel(def.Providers[0].Driver)
		}
		return fmt.Sprintf(" Substituted: %s → %s: %s needs %s.", choice.Tier, usedModel, def.ID, need)
	}
	return ""
}

// tierUnavailable returns a message when the chosen tier cannot be served by the
// user's providers (usedModel == "" means resolution failed outright), naming
// the tiers that can.
func (t *generateVideoTool) tierUnavailable(tc *rctx.ToolContext, userID string, choice mediaChoice, usedModel string) string {
	return videoKind.verifyTier(tc.Context, choice, usedModel, func(ctx context.Context, selector models.ModelSelector) (string, error) {
		generator, err := t.resolve(ctx, userID, selector)
		if err != nil {
			return "", err
		}
		return generator.Info().ModelID, nil
	})
}

// resume continues a render recorded by an earlier execution of this same tool
// call. It never Submits: the provider job is already running (or finished),
// and a second Submit is a second billed clip.
func (t *generateVideoTool) resume(tc *rctx.ToolContext, userID string, prior *videojobs.Job, params GenerateVideoParams) (ToolResponse, error) {
	if prior.UserID != userID {
		return NewTextErrorResponse("Video job unavailable."), nil
	}
	request := videogen.Request{
		Prompt: params.Prompt, DurationSeconds: params.DurationSeconds, Resolution: params.Resolution, AspectRatio: params.AspectRatio,
	}
	if request.Resolution == "" {
		request.Resolution = "720p"
	}
	providerJob := videogen.Job{ID: prior.ProviderJob}

	switch prior.State {
	case videojobs.StateCompleted:
		att, err := t.repo.GetAttachment(tc.Context, prior.AttachmentID)
		if err != nil || att == nil {
			return NewTextErrorResponse("This video was generated but its stored clip is no longer available."), nil
		}
		output := GenerateVideoOutput{
			AttachmentID: att.ID, Filename: att.Filename, MimeType: att.MimeType, Size: int(att.Size), Model: prior.ModelID,
			DurationSeconds: request.DurationSeconds, Resolution: request.Resolution, AspectRatio: request.AspectRatio,
			Prompt: request.Prompt, ProviderJob: prior.ProviderJob,
		}
		generator, err := t.resolve(tc.Context, userID, models.ModelSelector{ID: prior.ModelID, Providers: []string{prior.Driver}})
		if err == nil {
			if caps := generator.Info().Capabilities; caps != nil && caps.SupportsEdit {
				output.EditHandle = att.ID
			}
		}
		return WithResponseMetadata(NewTextResponse(videoSummary(output, "")), output), nil
	case videojobs.StateFailed, videojobs.StateCancelled:
		return NewTextErrorResponse(fmt.Sprintf("This video render already ended (%s): %s", prior.State, prior.ErrorMessage)), nil
	}

	generator, err := t.resolve(tc.Context, userID, models.ModelSelector{ID: prior.ModelID, Providers: []string{prior.Driver}})
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Could not resume the video render: %v", err)), nil
	}
	logging.Info("Resuming video render", "tool_call_id", prior.ToolCallID, "provider_job", prior.ProviderJob, "model", prior.ModelID)
	return t.await(tc, generator, prior, providerJob, params, request, prior.CreatedAt, chosenLine(prior.ModelID, "resumed render"))
}

// await polls a submitted job to its end and turns the outcome into a result.
func (t *generateVideoTool) await(tc *rctx.ToolContext, generator VideoGenerator, job *videojobs.Job, providerJob videogen.Job, params GenerateVideoParams, request videogen.Request, started time.Time, chosen string) (ToolResponse, error) {
	response, err := videogen.Wait(tc.Context, generator, providerJob, t.wait)
	if err != nil {
		return t.failure(tc, generator, job, providerJob, err), nil
	}
	if err := attachment.CheckGeneratedArtifactSize(len(response.Bytes)); err != nil {
		t.finish(tc, job, videojobs.StateFailed, err.Error())
		return NewTextErrorResponse(fmt.Sprintf("Video generation succeeded but the clip was rejected: %v", err)), nil
	}

	filename := generatedVideoFilename(response.MIMEType)
	attachmentID, err := t.storeAttachment(tc.Context, job.UserID, filename, response)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Generated the video but could not store it: %v (provider job %s is still available at the provider for about 2 days)", err, providerJob.ID)), nil
	}
	if job.ToolCallID != "" {
		if err := t.jobs.Complete(tc.Context, job.ToolCallID, attachmentID); err != nil {
			logging.Warn("Could not mark video job complete", "tool_call_id", job.ToolCallID, "error", err)
		}
	}

	output := GenerateVideoOutput{
		AttachmentID: attachmentID, Filename: filename, MimeType: response.MIMEType, Size: len(response.Bytes),
		Model: response.ModelID, DurationSeconds: request.DurationSeconds, Resolution: request.Resolution,
		AspectRatio: request.AspectRatio, Prompt: request.Prompt, ProviderJob: providerJob.ID,
		ElapsedSeconds: int(time.Since(started).Seconds()), Chosen: chosen,
	}
	if caps := generator.Info().Capabilities; caps != nil && caps.SupportsEdit {
		output.EditHandle = attachmentID
	}

	var saveNote string
	if strings.TrimSpace(params.SaveTo) != "" {
		savedTo, saveErr := t.saveToDaemon(tc, params, response.Bytes)
		if saveErr != nil {
			saveNote = fmt.Sprintf("\n\nWARNING: could not write the file to %s: %v", params.SaveTo, saveErr)
		} else {
			output.SavedTo = savedTo
			saveNote = fmt.Sprintf("\nSaved to: %s", savedTo)
		}
	}

	logging.Info("Generated video",
		"attachment_id", attachmentID, "model", response.ModelID, "driver", response.Driver,
		"bytes", len(response.Bytes), "provider_job", providerJob.ID, "elapsed_seconds", output.ElapsedSeconds)

	return WithResponseMetadata(NewTextResponse(videoSummary(output, saveNote)), output), nil
}

func videoSummary(o GenerateVideoOutput, saveNote string) string {
	var details []string
	details = append(details, o.MimeType)
	if o.DurationSeconds > 0 {
		details = append(details, fmt.Sprintf("%ds", o.DurationSeconds))
	}
	if o.Resolution != "" {
		res := o.Resolution
		if o.AspectRatio != "" {
			res += " " + o.AspectRatio
		}
		details = append(details, res)
	}
	details = append(details, fmt.Sprintf("%.1f MB", float64(o.Size)/(1<<20)))

	var b strings.Builder
	fmt.Fprintf(&b, "Generated %s (%s) with %s in %ds.\n", o.Filename, strings.Join(details, ", "), o.Model, o.ElapsedSeconds)
	if o.Chosen != "" {
		fmt.Fprintf(&b, "%s\n", o.Chosen)
	}
	fmt.Fprintf(&b, "Attachment id: %s", o.AttachmentID)
	if o.EditHandle != "" {
		fmt.Fprintf(&b, "\nEdit handle: %s   (pass as edit_from to refine this video)", o.EditHandle)
	}
	b.WriteString(saveNote)
	fmt.Fprintf(&b, "\nPrompt sent: %q", o.Prompt)
	b.WriteString("\nYou cannot see this video. Describe what you asked for, not what it shows, and ask the user to review it.")
	return b.String()
}

// failure maps a Wait error to a result and updates the job record. A timeout
// keeps the job submitted: it may still finish, and a re-ask can resume it.
func (t *generateVideoTool) failure(tc *rctx.ToolContext, generator VideoGenerator, job *videojobs.Job, providerJob videogen.Job, err error) ToolResponse {
	if tc.Context.Err() != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = generator.Cancel(cleanup, providerJob)
		if job.ToolCallID != "" {
			_ = t.jobs.Finish(cleanup, job.ToolCallID, videojobs.StateCancelled, "cancelled by the user")
		}
		return NewTextErrorResponse("Video generation was cancelled.")
	}
	var classified *videogen.Error
	if errors.As(err, &classified) && classified.Kind == videogen.KindTimeout {
		return NewTextErrorResponse(fmt.Sprintf("%s. Ask again to keep waiting for it (job %s).", classified.Message, providerJob.ID))
	}
	t.finish(tc, job, videojobs.StateFailed, err.Error())
	return NewTextErrorResponse(videoErrorMessage("Video generation failed", err))
}

func (t *generateVideoTool) finish(tc *rctx.ToolContext, job *videojobs.Job, state videojobs.State, message string) {
	if job.ToolCallID == "" {
		return
	}
	if err := t.jobs.Finish(tc.Context, job.ToolCallID, state, message); err != nil {
		logging.Warn("Could not record video job outcome", "tool_call_id", job.ToolCallID, "error", err)
	}
}

func videoErrorMessage(prefix string, err error) string {
	return fmt.Sprintf("%s: %v", prefix, err)
}

// lookupEdit resolves an edit handle to the completed job that produced it,
// refusing anything the caller does not own.
func (t *generateVideoTool) lookupEdit(ctx context.Context, userID, handle string) (*videojobs.Job, *ToolResponse) {
	const unavailable = "Edit handle unavailable; regenerate with a full prompt instead."
	prior, err := t.jobs.GetByAttachment(ctx, handle)
	if err != nil || prior == nil || prior.UserID != userID || prior.State != videojobs.StateCompleted {
		resp := NewTextErrorResponse(unavailable)
		return nil, &resp
	}
	return prior, nil
}

// buildRequest validates against the model's declared capabilities and loads
// the input images. It returns a user-facing message instead of a request when
// the call cannot be made.
func (t *generateVideoTool) buildRequest(ctx context.Context, userID string, params GenerateVideoParams, edit *videojobs.Job, info videogen.ModelInfo) (videogen.Request, string) {
	request := videogen.Request{
		Prompt:          params.Prompt,
		DurationSeconds: params.DurationSeconds,
		Resolution:      params.Resolution,
		AspectRatio:     params.AspectRatio,
		NegativePrompt:  params.NegativePrompt,
		Audio:           params.Audio,
	}
	if request.Resolution == "" {
		request.Resolution = "720p"
	}
	if edit != nil {
		request.EditOf = edit.ProviderJob
	}
	if params.EndFrame != "" && params.StartFrame == "" {
		return request, "end_frame requires start_frame"
	}

	var err error
	if params.StartFrame != "" {
		if request.StartFrame, err = t.loadImage(ctx, userID, params.StartFrame); err != nil {
			return request, err.Error()
		}
	}
	if params.EndFrame != "" {
		if request.EndFrame, err = t.loadImage(ctx, userID, params.EndFrame); err != nil {
			return request, err.Error()
		}
	}
	for _, id := range params.ReferenceImages {
		image, err := t.loadImage(ctx, userID, id)
		if err != nil {
			return request, err.Error()
		}
		request.References = append(request.References, *image)
	}

	if err := info.Capabilities.ValidateVideoRequest(info.ModelID, models.VideoRequestParams{
		DurationSeconds: params.DurationSeconds,
		Resolution:      request.Resolution,
		AspectRatio:     params.AspectRatio,
		ReferenceImages: len(params.ReferenceImages),
		NegativePrompt:  params.NegativePrompt != "",
		Edit:            edit != nil,
	}); err != nil {
		msg := err.Error()
		if registry, regErr := models.GetRegistry(); regErr == nil && edit == nil {
			msg += videoTierHint(registry, tierOf(registry, info.ModelID), models.VideoRequestParams{
				DurationSeconds: params.DurationSeconds,
				Resolution:      request.Resolution,
				AspectRatio:     params.AspectRatio,
				ReferenceImages: len(params.ReferenceImages),
				NegativePrompt:  params.NegativePrompt != "",
			})
		}
		return request, msg
	}
	return request, ""
}

// tierOf names the video tier a model belongs to, "" when none.
func tierOf(registry *models.ModelRegistry, modelID string) string {
	tags := registry.TagsOf(modelID)
	for _, tier := range videoKind.tiers {
		if tier.Requires != "" && slices.Contains(tags, tier.Requires) {
			return tier.Name
		}
	}
	if slices.Contains(tags, models.TagFlagship) {
		return "standard"
	}
	return ""
}

func (t *generateVideoTool) loadImage(ctx context.Context, userID, attachmentID string) (*videogen.Image, error) {
	att, err := t.repo.GetAttachment(ctx, attachmentID)
	if err != nil || att == nil || att.UserID != userID {
		return nil, fmt.Errorf("attachment %s was not found", attachmentID)
	}
	if att.AttachmentType != string(attachment.TypeImage) || len(att.Content) == 0 {
		return nil, fmt.Errorf("attachment %s is not an image", attachmentID)
	}
	return &videogen.Image{Bytes: att.Content, MIMEType: att.MimeType}, nil
}

func (t *generateVideoTool) userID(tc *rctx.ToolContext) string {
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

// storeAttachment persists the clip in the database, the one substrate every
// process can read in distributed mode.
func (t *generateVideoTool) storeAttachment(ctx context.Context, userID, filename string, response *videogen.Response) (string, error) {
	attachmentID := uuid.New().String()
	hash := sha256.Sum256(response.Bytes)
	fileHash := hex.EncodeToString(hash[:])
	now := time.Now().UTC()
	att := &db.Attachment{
		ID: attachmentID, UserID: userID, Filename: filename, Size: int64(len(response.Bytes)),
		MimeType: response.MIMEType, FileHash: &fileHash, FilePath: filepath.Join(userID, filename),
		AttachmentType: string(attachment.TypeVideo), Content: response.Bytes, CreatedAt: now, UpdatedAt: now,
	}
	if err := t.repo.CreateAttachment(ctx, att); err != nil {
		return "", err
	}
	return attachmentID, nil
}

// saveToDaemon writes the clip to the user's filesystem. WriteBinaryFile, never
// WriteFile: the text path substitutes U+FFFD for non-UTF-8 bytes and silently
// destroys an mp4.
func (t *generateVideoTool) saveToDaemon(tc *rctx.ToolContext, params GenerateVideoParams, content []byte) (string, error) {
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

// generatedVideoFilename names the file after the container the bytes carry;
// attachment.GetAttachmentType classifies by extension.
func generatedVideoFilename(mimeType string) string {
	extension := ".mp4"
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "video/webm":
		extension = ".webm"
	case "video/quicktime":
		extension = ".mov"
	}
	return "generated-" + uuid.New().String()[:8] + extension
}
