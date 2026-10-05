// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Config describes one credentialed video endpoint. Everything in it is decided
// by the caller (drivers.ResolveVideoGenerator): this package does no model
// resolution and reads no environment.
type Config struct {
	// APIKey is the AI Studio key. Unused on Vertex, where HTTPClient carries
	// the credential.
	APIKey string
	// BaseURL overrides the provider root; empty means the SDK default.
	BaseURL      string
	ExtraHeaders map[string]string

	// ModelID is the Reliant registry model id; APIModel is the provider's.
	ModelID  string
	APIModel string
	Driver   string
	// Capabilities are the model's declared video capabilities, surfaced via
	// Info so the caller validates requests against them.
	Capabilities *models.VideoCapabilities

	// HTTPClient is optional for AI Studio. On Vertex it MUST authenticate.
	HTTPClient *http.Client
	// Vertex, when set, targets Vertex AI instead of AI Studio.
	Vertex *VertexConfig
	// RetryBaseDelay is optional; zero means defaultSubmitRetryDelay.
	RetryBaseDelay time.Duration
}

// VertexConfig selects the Vertex AI backend.
type VertexConfig struct {
	Project  string
	Location string
}

// GenAIClientFactory builds the genai client this package calls through. It is
// a parameter because the vendor constructor has no response-body idle timeout
// and every SDK client in this repo is built by llm.NewGenAISDKClient, which
// this package cannot import without a cycle (see imagegen.GenAIClientFactory).
type GenAIClientFactory func(ctx context.Context, config *genai.ClientConfig) (*genai.Client, error)

// veoModels, veoOperations and veoFiles are the three SDK surfaces the client
// needs, declared here so tests fake them without a network round trip.
type veoModels interface {
	GenerateVideosFromSource(ctx context.Context, model string, source *genai.GenerateVideosSource, config *genai.GenerateVideosConfig) (*genai.GenerateVideosOperation, error)
}

type veoOperations interface {
	GetVideosOperation(ctx context.Context, operation *genai.GenerateVideosOperation, config *genai.GetOperationConfig) (*genai.GenerateVideosOperation, error)
}

type veoFiles interface {
	Download(ctx context.Context, uri genai.DownloadURI, config *genai.DownloadFileConfig) ([]byte, error)
}

// VeoClient generates video with Veo through predictLongRunning, on either AI
// Studio (a user's gemini key) or Vertex AI.
type VeoClient struct {
	config Config
	models veoModels
	ops    veoOperations
	files  veoFiles
	vertex bool
}

// NewVeo builds a Veo client using the supplied SDK constructor.
func NewVeo(config Config, newClient GenAIClientFactory) (*VeoClient, error) {
	if newClient == nil {
		return nil, fmt.Errorf("veo video generation requires an SDK client constructor")
	}
	if config.Vertex == nil && strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("veo video generation requires a Gemini API key")
	}
	sdkClient, err := newClient(context.Background(), veoClientConfig(config))
	if err != nil {
		return nil, fmt.Errorf("failed to create Veo client: %w", err)
	}
	return &VeoClient{config: config, models: sdkClient.Models, ops: sdkClient.Operations, files: sdkClient.Files, vertex: config.Vertex != nil}, nil
}

func newVeoWithFakes(config Config, m veoModels, o veoOperations, f veoFiles) *VeoClient {
	return &VeoClient{config: config, models: m, ops: o, files: f, vertex: config.Vertex != nil}
}

func veoClientConfig(config Config) *genai.ClientConfig {
	httpOptions := genai.HTTPOptions{}
	if baseURL := strings.TrimSpace(config.BaseURL); baseURL != "" {
		httpOptions.BaseURL = baseURL
	}
	if len(config.ExtraHeaders) > 0 {
		httpOptions.Headers = make(http.Header, len(config.ExtraHeaders))
		for key, value := range config.ExtraHeaders {
			httpOptions.Headers.Set(key, value)
		}
	}
	cc := &genai.ClientConfig{HTTPClient: config.HTTPClient, HTTPOptions: httpOptions}
	if config.Vertex != nil {
		cc.Backend = genai.BackendVertexAI
		cc.Project = config.Vertex.Project
		cc.Location = config.Vertex.Location
		return cc
	}
	cc.Backend = genai.BackendGeminiAPI
	cc.APIKey = config.APIKey
	return cc
}

// Info reports the bound model and its declared capabilities.
func (c *VeoClient) Info() ModelInfo {
	return ModelInfo{ModelID: c.config.ModelID, APIModel: c.config.APIModel, Driver: c.config.Driver, Capabilities: c.config.Capabilities}
}

// Submit starts a render and returns the provider operation name.
func (c *VeoClient) Submit(ctx context.Context, request Request) (Job, error) {
	if strings.TrimSpace(request.Prompt) == "" && request.StartFrame == nil {
		return Job{}, &Error{Kind: KindInvalid, Message: "video generation requires a prompt"}
	}
	if request.EditOf != "" {
		return Job{}, &Error{Kind: KindInvalid, Message: fmt.Sprintf("model %s cannot edit a previous video", c.config.ModelID)}
	}
	source, genConfig := c.buildRequest(request)

	return submitWithRetry(ctx, c.config.RetryBaseDelay, func(ctx context.Context) (Job, int, error) {
		operation, err := c.models.GenerateVideosFromSource(ctx, c.config.APIModel, source, genConfig)
		if err != nil {
			status, classified := veoError(err)
			return Job{}, status, classified
		}
		if operation == nil || operation.Name == "" {
			return Job{}, 0, &Error{Kind: KindFailed, Message: "the provider accepted the request but returned no operation name"}
		}
		return Job{Provider: "veo", ID: operation.Name}, 0, nil
	})
}

func (c *VeoClient) buildRequest(request Request) (*genai.GenerateVideosSource, *genai.GenerateVideosConfig) {
	source := &genai.GenerateVideosSource{Prompt: request.Prompt}
	if request.StartFrame != nil {
		source.Image = genaiImage(request.StartFrame)
	}
	genConfig := &genai.GenerateVideosConfig{
		NumberOfVideos:  1,
		AspectRatio:     request.AspectRatio,
		Resolution:      request.Resolution,
		NegativePrompt:  request.NegativePrompt,
		DurationSeconds: nil,
	}
	if request.DurationSeconds > 0 {
		duration := int32(request.DurationSeconds)
		genConfig.DurationSeconds = &duration
	}
	if request.EndFrame != nil {
		genConfig.LastFrame = genaiImage(request.EndFrame)
	}
	for i := range request.References {
		genConfig.ReferenceImages = append(genConfig.ReferenceImages, &genai.VideoGenerationReferenceImage{
			Image:         genaiImage(&request.References[i]),
			ReferenceType: genai.VideoGenerationReferenceTypeAsset,
		})
	}
	// generateAudio is rejected outright by the Gemini API backend, where Veo
	// 3.1 always renders audio, so it is only forwarded on Vertex.
	if c.vertex && request.Audio != nil {
		audio := *request.Audio
		genConfig.GenerateAudio = &audio
	}
	return source, genConfig
}

func genaiImage(image *Image) *genai.Image {
	return &genai.Image{ImageBytes: image.Bytes, MIMEType: image.MIMEType}
}

// Poll checks one operation. It never submits.
func (c *VeoClient) Poll(ctx context.Context, job Job) (*Response, bool, error) {
	operation, err := c.ops.GetVideosOperation(ctx, &genai.GenerateVideosOperation{Name: job.ID}, nil)
	if err != nil {
		var apiErr genai.APIError
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
			return nil, false, &Error{Kind: KindExpired, Message: fmt.Sprintf("video job %s no longer exists at the provider (jobs expire after about 2 days)", job.ID), Job: job}
		}
		// Deliberately unclassified: the job is running regardless, so Wait
		// tolerates a few transient poll failures in a row.
		return nil, false, fmt.Errorf("polling video job: %w", err)
	}
	if operation == nil || !operation.Done {
		return nil, false, nil
	}
	if len(operation.Error) > 0 {
		return nil, false, &Error{Kind: KindFailed, Message: "video generation failed: " + operationErrorMessage(operation.Error), Job: job}
	}
	return c.finish(ctx, job, operation.Response)
}

func (c *VeoClient) finish(ctx context.Context, job Job, response *genai.GenerateVideosResponse) (*Response, bool, error) {
	if response == nil || len(response.GeneratedVideos) == 0 || response.GeneratedVideos[0] == nil || response.GeneratedVideos[0].Video == nil {
		message := "the provider finished but returned no video"
		if response != nil && response.RAIMediaFilteredCount > 0 {
			message = fmt.Sprintf("the video was withheld by a content filter (%d filtered)", response.RAIMediaFilteredCount)
			if len(response.RAIMediaFilteredReasons) > 0 {
				message += ": " + strings.Join(response.RAIMediaFilteredReasons, "; ")
			}
			return nil, false, &Error{Kind: KindFiltered, Message: message, Job: job}
		}
		return nil, false, &Error{Kind: KindFailed, Message: message, Job: job}
	}

	generated := response.GeneratedVideos[0]
	data := generated.Video.VideoBytes
	if len(data) == 0 {
		if generated.Video.URI == "" {
			return nil, false, &Error{Kind: KindFailed, Message: "the provider returned a video with neither bytes nor a download URI", Job: job}
		}
		downloaded, err := c.files.Download(ctx, genai.NewDownloadURIFromGeneratedVideo(generated), nil)
		if err != nil {
			// Not classified: the clip exists for 2 days, so Wait retries the
			// poll, which re-reads the operation and downloads again.
			return nil, false, fmt.Errorf("downloading the generated video: %w", err)
		}
		data = downloaded
	}
	if len(data) == 0 {
		return nil, false, &Error{Kind: KindFailed, Message: "the provider returned an empty video", Job: job}
	}
	mimeType := generated.Video.MIMEType
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	return &Response{Bytes: data, MIMEType: mimeType, ModelID: c.config.ModelID, APIModel: c.config.APIModel, Driver: c.config.Driver, Job: job}, true, nil
}

// Cancel is a no-op: Veo operations cannot be cancelled, and a clip that
// completes is billed regardless.
func (c *VeoClient) Cancel(ctx context.Context, job Job) error { return nil }

func operationErrorMessage(operationError map[string]any) string {
	if message, ok := operationError["message"].(string); ok && message != "" {
		return message
	}
	return fmt.Sprint(operationError)
}

// veoError normalizes an SDK error into a classified *Error where the status
// says what happened, and returns the HTTP status for the submit retry ladder.
func veoError(err error) (int, error) {
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return 0, fmt.Errorf("video generation request failed: %w", err)
	}
	message := strings.TrimSpace(apiErr.Message)
	if message == "" {
		message = strings.TrimSpace(apiErr.Error())
	}
	kind := KindFailed
	switch {
	case apiErr.Code == http.StatusTooManyRequests || strings.EqualFold(apiErr.Status, "RESOURCE_EXHAUSTED"):
		kind = KindQuota
		message = "video quota exhausted for this provider; try again later or switch model: " + message
	case apiErr.Code == http.StatusBadRequest:
		kind = KindInvalid
	}
	return apiErr.Code, &Error{Kind: kind, Message: message}
}
