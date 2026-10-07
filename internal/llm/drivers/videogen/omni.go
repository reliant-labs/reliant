// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/apierrors"
	gaos "google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
)

// omniInteractions is the slice of client.Interactions this client uses,
// declared here so tests fake it without a network round trip.
type omniInteractions interface {
	Create(ctx context.Context, request operations.CreateInteractionRequest, opts ...operations.Option) (*operations.CreateInteractionResponse, error)
	Get(ctx context.Context, request operations.GetInteractionByIDRequest, opts ...operations.Option) (*operations.GetInteractionByIDResponse, error)
	Cancel(ctx context.Context, request operations.CancelInteractionByIDRequest, opts ...operations.Option) (*operations.CancelInteractionByIDResponse, error)
}

type omniFiles interface {
	Get(ctx context.Context, name string, config *genai.GetFileConfig) (*genai.File, error)
	Download(ctx context.Context, uri genai.DownloadURI, config *genai.DownloadFileConfig) ([]byte, error)
}

// omniBackground selects create-then-poll. UNVERIFIED against the live API for
// video: Google's guide documents background=true for long-running Interactions
// in general and recommends background=false only for synchronous use, and no
// live Omni call has been made. Setting it false degrades gracefully: Create
// then blocks for the render, returns a completed interaction, and Poll reads
// it straight back, at the cost of losing resume for a worker that dies inside
// that one request.
const omniBackground = true

// OmniClient generates video with Gemini Omni through the Interactions API.
//
// Submit creates the interaction with background=true, which returns its id
// immediately instead of holding one HTTP request open for the whole render;
// Poll then reads it back. That keeps the Submit/Poll shape (and so resume
// after a worker restart) identical to Veo, and keeps the long wait out of any
// single request's idle timeout.
type OmniClient struct {
	config       Config
	interactions omniInteractions
	files        omniFiles
}

// NewOmni builds an Omni client using the supplied SDK constructor. Interactions
// are not available on Vertex, so this is AI Studio only.
func NewOmni(config Config, newClient GenAIClientFactory) (*OmniClient, error) {
	if newClient == nil {
		return nil, fmt.Errorf("omni video generation requires an SDK client constructor")
	}
	if config.Vertex != nil {
		return nil, fmt.Errorf("the Interactions API is not available on Vertex AI")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("omni video generation requires a Gemini API key")
	}
	sdkClient, err := newClient(context.Background(), veoClientConfig(config))
	if err != nil {
		return nil, fmt.Errorf("failed to create Omni client: %w", err)
	}
	return &OmniClient{config: config, interactions: sdkClient.Interactions, files: sdkClient.Files}, nil
}

func newOmniWithFakes(config Config, i omniInteractions, f omniFiles) *OmniClient {
	return &OmniClient{config: config, interactions: i, files: f}
}

// Info reports the bound model and its declared capabilities.
func (c *OmniClient) Info() ModelInfo {
	return ModelInfo{ModelID: c.config.ModelID, APIModel: c.config.APIModel, Driver: c.config.Driver, Capabilities: c.config.Capabilities}
}

// Submit starts a render and returns the interaction id.
func (c *OmniClient) Submit(ctx context.Context, request Request) (Job, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return Job{}, &Error{Kind: KindInvalid, Message: "video generation requires a prompt"}
	}
	body := c.buildRequest(request)

	return submitWithRetry(ctx, c.config.RetryBaseDelay, func(ctx context.Context) (Job, int, error) {
		response, err := c.interactions.Create(ctx, operations.CreateInteractionRequest{
			Body: operations.NewCreateInteractionRequestBody(body),
		})
		if err != nil {
			status, classified := omniError(err)
			return Job{}, status, classified
		}
		if response == nil || response.Interaction == nil || response.Interaction.ID == nil || *response.Interaction.ID == "" {
			return Job{}, 0, &Error{Kind: KindFailed, Message: "the provider accepted the request but returned no interaction id"}
		}
		return Job{Provider: "omni", ID: *response.Interaction.ID}, 0, nil
	})
}

func (c *OmniClient) buildRequest(request Request) gaos.CreateModelInteraction {
	format := gaos.VideoResponseFormat{
		// URI delivery is Google's recommendation for clips over 4MB. Poll
		// handles both shapes, because GET /interactions/{id} currently
		// returns inline data regardless of what was requested here.
		Delivery: gaos.VideoResponseFormatDeliveryURI.ToPointer(),
	}
	if request.AspectRatio != "" {
		aspect := gaos.VideoResponseFormatAspectRatio(request.AspectRatio)
		format.AspectRatio = &aspect
	}
	if request.Resolution != "" {
		resolution := gaos.Resolution(request.Resolution)
		format.Resolution = &resolution
	}
	if request.DurationSeconds > 0 {
		duration := fmt.Sprintf("%ds", request.DurationSeconds)
		format.Duration = &duration
	}

	body := gaos.CreateModelInteraction{
		Model:      gaos.Model(c.config.APIModel),
		Background: genai.Ptr(omniBackground),
		// store=true is what makes previous_interaction_id (conversational
		// edit) work, and background=true requires it.
		Store: genai.Ptr(true),
		// response_format is NOT inherited across turns, so it is sent on
		// every request, edits included.
		ResponseFormat: genai.Ptr(gaos.NewCreateModelInteractionResponseFormat(gaos.NewResponseFormat(format))),
	}
	if request.EditOf != "" {
		body.PreviousInteractionID = genai.Ptr(request.EditOf)
	}

	prompt := omniPrompt(request)
	images := omniImages(request)
	if len(images) == 0 {
		input := gaos.NewInteractionsInput(prompt)
		body.Input = &input
	} else {
		parts := append(images, gaos.NewContent(gaos.TextContent{Text: prompt}))
		input := gaos.NewInteractionsInput(parts)
		body.Input = &input
		task := gaos.TaskImageToVideo
		if len(request.References) > 0 {
			task = gaos.TaskReferenceToVideo
		}
		body.GenerationConfig = &gaos.GenerationConfig{VideoConfig: &gaos.VideoConfig{Task: &task}}
	}
	return body
}

// omniPrompt folds the options Omni has no field for into the prompt, which is
// the mechanism Google documents for them ("Do not X", "No dialogue").
func omniPrompt(request Request) string {
	prompt := strings.TrimSpace(request.Prompt)
	if negative := strings.TrimSpace(request.NegativePrompt); negative != "" {
		prompt += "\nDo not: " + negative + "."
	}
	if request.Audio != nil && !*request.Audio {
		prompt += "\nNo audio, no dialogue, no sound effects."
	}
	return prompt
}

func omniImages(request Request) []gaos.Content {
	var ordered []Image
	if request.StartFrame != nil {
		ordered = append(ordered, *request.StartFrame)
	}
	if request.EndFrame != nil {
		ordered = append(ordered, *request.EndFrame)
	}
	ordered = append(ordered, request.References...)

	contents := make([]gaos.Content, 0, len(ordered)+1)
	for _, image := range ordered {
		data := base64.StdEncoding.EncodeToString(image.Bytes)
		mimeType := gaos.ImageContentMimeType(image.MIMEType)
		contents = append(contents, gaos.NewContent(gaos.ImageContent{Data: &data, MimeType: &mimeType}))
	}
	return contents
}

// Poll reads the interaction back. It never submits.
func (c *OmniClient) Poll(ctx context.Context, job Job) (*Response, bool, error) {
	response, err := c.interactions.Get(ctx, operations.GetInteractionByIDRequest{ID: job.ID})
	if err != nil {
		var api *apierrors.APIError
		if errors.As(err, &api) && api.StatusCode == http.StatusNotFound {
			return nil, false, &Error{Kind: KindExpired, Message: fmt.Sprintf("video interaction %s no longer exists at the provider (interactions are kept 55 days on the paid tier, 1 day on the free tier)", job.ID), Job: job}
		}
		// Deliberately unclassified, so Wait tolerates a few transient failures.
		return nil, false, fmt.Errorf("polling video interaction: %w", err)
	}
	if response == nil || response.Interaction == nil {
		return nil, false, fmt.Errorf("polling video interaction: empty response")
	}
	interaction := response.Interaction

	switch interaction.Status {
	case gaos.InteractionStatusCompleted:
		return c.finish(ctx, job, interaction)
	case gaos.InteractionStatusFailed, gaos.InteractionStatusCancelled, gaos.InteractionStatusIncomplete, gaos.InteractionStatusBudgetExceeded:
		return nil, false, &Error{Kind: KindFailed, Message: fmt.Sprintf("video generation %s: %s", interaction.Status, interactionErrorText(interaction)), Job: job}
	default:
		return nil, false, nil
	}
}

func (c *OmniClient) finish(ctx context.Context, job Job, interaction *gaos.Interaction) (*Response, bool, error) {
	video := interactionVideo(interaction)
	if video == nil {
		message := interactionErrorText(interaction)
		if message == "" {
			message = "the provider finished but returned no video"
		}
		// A completed interaction with no video is how a content filter shows
		// up; never report it as a bare "no video".
		return nil, false, &Error{Kind: KindFiltered, Message: "no video was produced: " + message, Job: job}
	}

	var data []byte
	switch {
	case video.Data != nil && *video.Data != "":
		decoded, err := base64.StdEncoding.DecodeString(*video.Data)
		if err != nil {
			return nil, false, &Error{Kind: KindFailed, Message: "the provider returned undecodable video data", Job: job}
		}
		data = decoded
	case video.URI != nil && *video.URI != "":
		downloaded, ready, err := c.downloadFile(ctx, job, *video.URI)
		if err != nil || !ready {
			return nil, false, err
		}
		data = downloaded
	default:
		return nil, false, &Error{Kind: KindFailed, Message: "the provider returned a video with neither data nor a URI", Job: job}
	}
	if len(data) == 0 {
		return nil, false, &Error{Kind: KindFailed, Message: "the provider returned an empty video", Job: job}
	}

	mimeType := "video/mp4"
	if video.MimeType != nil && *video.MimeType != "" {
		mimeType = string(*video.MimeType)
	}
	return &Response{Bytes: data, MIMEType: mimeType, ModelID: c.config.ModelID, APIModel: c.config.APIModel, Driver: c.config.Driver, Job: job}, true, nil
}

// downloadFile fetches a Files API URI once its file is ACTIVE. A file still
// PROCESSING reports not-ready so Wait polls again.
func (c *OmniClient) downloadFile(ctx context.Context, job Job, uri string) ([]byte, bool, error) {
	name := uri[strings.LastIndex(uri, "/")+1:]
	if idx := strings.IndexAny(name, ":?"); idx >= 0 {
		name = name[:idx]
	}
	file, err := c.files.Get(ctx, "files/"+name, nil)
	if err != nil {
		return nil, false, fmt.Errorf("checking the generated video file: %w", err)
	}
	switch file.State {
	case genai.FileStateFailed:
		return nil, false, &Error{Kind: KindFailed, Message: "the generated video file failed to process", Job: job}
	case genai.FileStateActive:
	default:
		return nil, false, nil
	}
	data, err := c.files.Download(ctx, genai.NewDownloadURIFromFile(file), nil)
	if err != nil {
		return nil, false, fmt.Errorf("downloading the generated video: %w", err)
	}
	return data, true, nil
}

// interactionVideo finds the generated clip: the SDK's output_video shortcut
// first, then the model_output steps it is derived from.
func interactionVideo(interaction *gaos.Interaction) *gaos.VideoContent {
	if interaction.OutputVideo != nil {
		return interaction.OutputVideo
	}
	for _, step := range interaction.Steps {
		if step.ModelOutputStep == nil {
			continue
		}
		for _, content := range step.ModelOutputStep.Content {
			if content.VideoContent != nil {
				return content.VideoContent
			}
		}
	}
	return nil
}

func interactionErrorText(interaction *gaos.Interaction) string {
	var parts []string
	for _, e := range interaction.Errors {
		if e.Message != nil && *e.Message != "" {
			parts = append(parts, *e.Message)
		}
	}
	for _, step := range interaction.Steps {
		if step.ModelOutputStep != nil && step.ModelOutputStep.Error != nil && step.ModelOutputStep.Error.Message != nil {
			parts = append(parts, *step.ModelOutputStep.Error.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// Cancel asks the provider to stop the interaction. Best effort.
func (c *OmniClient) Cancel(ctx context.Context, job Job) error {
	_, err := c.interactions.Cancel(ctx, operations.CancelInteractionByIDRequest{ID: job.ID})
	return err
}

// omniError normalizes an SDK error into a classified *Error and returns the
// HTTP status for the submit retry ladder.
func omniError(err error) (int, error) {
	var api *apierrors.APIError
	if !errors.As(err, &api) {
		return 0, fmt.Errorf("video generation request failed: %w", err)
	}
	message := strings.TrimSpace(api.Message)
	kind := KindFailed
	switch api.StatusCode {
	case http.StatusTooManyRequests:
		kind = KindQuota
		message = "video quota exhausted for this provider; try again later or switch model: " + message
	case http.StatusBadRequest:
		kind = KindInvalid
	}
	return api.StatusCode, &Error{Kind: kind, Message: message}
}
