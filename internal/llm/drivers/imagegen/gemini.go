// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// GeminiClient generates images against Google AI Studio's native API.
//
// It exists as a SECOND client type rather than a response-shape branch inside
// Client because nothing about the two calls is shared below the retry loop:
// the endpoint is <model>:generateContent rather than /images/generations, the
// credential is an x-goog-api-key header rather than a Bearer token, the
// request is a Content/Part tree rather than a flat prompt object, and the
// response carries images as candidates[].content.parts[].inlineData rather
// than data[].b64_json. A branch would have had to fork every one of those,
// leaving a struct whose fields half apply and a decoder with two disjoint
// halves. What the two genuinely share — the retry ladder, the Image/Response
// value types, the APIError normalization — is shared as functions in this
// package instead.
//
// Both clients return the same *Response from the same GenerateImage
// signature, which is what the generate_image tool's locally-declared
// one-method interface is written against, so the tool calls either one
// unchanged.
type GeminiClient struct {
	config Config
	models geminiImageModels
}

// geminiImageModels is the one thing this client needs from the genai SDK.
// Declared at the consumer so the SDK can be faked in tests without a live
// call and without an httptest server, and so this file states its dependency
// as one method rather than the whole *genai.Client surface.
type geminiImageModels interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// GenAIClientFactory builds the genai client this package calls through.
//
// It is a parameter rather than a direct genai.NewClient call because the
// vendor constructor defaults to an http.Client with no idle timeout on the
// response body, and this repo funnels every SDK client through
// llm.NewGenAISDKClient to install one. That rule is enforced statically by
// TestDriversUseSanctionedSDKConstructors, and this package cannot import
// internal/llm to satisfy it — internal/llm already depends on this package
// (through llm/tools), so the edge back would be a cycle.
//
// So the caller supplies the constructor. drivers.newImageGenClient passes
// llm.NewGenAISDKClient, which is exactly the sanctioned one.
type GenAIClientFactory func(ctx context.Context, config *genai.ClientConfig) (*genai.Client, error)

// GeminiConfig describes AI Studio's client-construction inputs, which the
// genai SDK takes as a struct rather than as request options.
//
// The API key is deliberately NOT sent as the `?key=` query parameter used
// when verifying this path by hand: the SDK sends it as an `x-goog-api-key`
// header, AI Studio accepts either, and the header is the form the chat
// drivers in this repo already rely on.
func geminiClientConfig(config Config) *genai.ClientConfig {
	httpOptions := genai.HTTPOptions{}
	if baseURL := strings.TrimSpace(config.BaseURL); baseURL != "" {
		httpOptions.BaseURL = baseURL
	}
	if len(config.ExtraHeaders) > 0 {
		httpOptions.Headers = make(map[string][]string, len(config.ExtraHeaders))
		for key, value := range config.ExtraHeaders {
			httpOptions.Headers.Set(key, value)
		}
	}

	return &genai.ClientConfig{
		APIKey:      config.APIKey,
		Backend:     genai.BackendGeminiAPI,
		HTTPClient:  config.HTTPClient,
		HTTPOptions: httpOptions,
	}
}

// NewGemini builds a client for AI Studio using the supplied SDK constructor.
//
// This uses the google.golang.org/genai SDK rather than hand-rolled HTTP. The
// SDK is already a direct dependency (the gemini and vertexai chat drivers use
// it) and it composes the exact endpoint the native envelope was verified on:
// POST {base}/v1beta/models/<model>:generateContent.
//
// Deliberately NOT the SDK's Models.GenerateImages: that posts to :predict,
// which is the Imagen surface. The Gemini image family (gemini-3.1-flash-image
// and friends) is served by :generateContent returning inline image parts, and
// the SDK has marked GenerateImages deprecated in favor of exactly this call.
func NewGemini(config Config, newClient GenAIClientFactory) (*GeminiClient, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("gemini image generation requires an API key")
	}
	if newClient == nil {
		return nil, fmt.Errorf("gemini image generation requires an SDK client constructor")
	}

	// The SDK takes a context for credential detection, which the Gemini API
	// backend does not perform — a static API key needs no discovery — so
	// there is nothing here for a caller's context to cancel.
	sdkClient, err := newClient(context.Background(), geminiClientConfig(config))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini image client: %w", err)
	}

	return &GeminiClient{config: config, models: sdkClient.Models}, nil
}

// newGeminiWithModels builds a client over an arbitrary model caller. Used by
// tests to substitute the SDK without a network round trip.
func newGeminiWithModels(config Config, models geminiImageModels) *GeminiClient {
	return &GeminiClient{config: config, models: models}
}

// ModelID returns the Reliant registry model id this client is bound to.
func (c *GeminiClient) ModelID() string { return c.config.ModelID }

// Driver returns the provider driver id this client routes through.
func (c *GeminiClient) Driver() string { return c.config.Driver }

// GenerateImage performs one generation call against AI Studio and returns the
// raw image bytes.
func (c *GeminiClient) GenerateImage(ctx context.Context, request Request) (*Response, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, fmt.Errorf("image generation requires a prompt")
	}

	return generateWithRetry(ctx, c.config.ModelID, c.config.Driver, c.config.RetryBaseDelay,
		func(ctx context.Context) (*Response, error) { return c.attempt(ctx, request) })
}

func (c *GeminiClient) attempt(ctx context.Context, request Request) (*Response, error) {
	response, err := c.models.GenerateContent(ctx,
		c.config.APIModel,
		[]*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: request.Prompt}}}},
		c.generateConfig(request),
	)
	if err != nil {
		return nil, geminiAPIError(err)
	}

	images, err := decodeGeminiImages(response)
	if err != nil {
		return nil, err
	}

	return &Response{
		Images:   images,
		ModelID:  c.config.ModelID,
		APIModel: c.config.APIModel,
		Driver:   c.config.Driver,
	}, nil
}

// generateConfig maps a Request onto the generateContent config.
//
// ResponseModalities is pinned to IMAGE because that is what makes this an
// image call at all: without it the model answers in text and the response
// carries no inlineData part.
//
// Request.Size is an OpenAI pixel string ("1024x1024") and AI Studio takes an
// aspect ratio plus a size tier, so the two are translated rather than passed
// through. An unrecognized value is dropped rather than forwarded: the SDK
// rejects an unknown aspectRatio outright, which would turn a cosmetic
// mismatch into a failed generation, and omitting it lets the model default.
//
// Quality and Background have no AI Studio equivalent on this surface and are
// deliberately ignored — Request documents its optional fields as hints that
// defer to the provider.
func (c *GeminiClient) generateConfig(request Request) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{string(genai.ModalityImage)},
	}
	if aspectRatio := geminiAspectRatio(request.Size); aspectRatio != "" {
		config.ImageConfig = &genai.ImageConfig{AspectRatio: aspectRatio}
	}
	return config
}

// geminiAspectRatio converts the size strings the generate_image tool offers
// into the aspect ratios AI Studio accepts. Returns "" for anything else,
// including "auto", which means "let the model decide" in both vocabularies.
func geminiAspectRatio(size string) string {
	switch strings.TrimSpace(strings.ToLower(size)) {
	case "1024x1024":
		return "1:1"
	case "1536x1024":
		return "3:2"
	case "1024x1536":
		return "2:3"
	default:
		return ""
	}
}

// decodeGeminiImages pulls image bytes out of the native envelope:
// candidates[].content.parts[].inlineData{mimeType,data}.
//
// A part may carry text instead of an image — the model can return commentary
// alongside or instead of a generation. Text is ignored when an image is
// present and surfaced in the error when none is, because in that case it is
// the only explanation of why nothing was generated.
func decodeGeminiImages(response *genai.GenerateContentResponse) ([]Image, error) {
	if response == nil {
		return nil, &APIError{EmptyResult: true, Message: noImagesMessage}
	}

	var images []Image
	var narration []string
	var finishReasons []string

	if feedback := response.PromptFeedback; feedback != nil {
		if reason := strings.TrimSpace(string(feedback.BlockReason)); reason != "" {
			finishReasons = append(finishReasons, "prompt blocked: "+reason)
		}
		if message := strings.TrimSpace(feedback.BlockReasonMessage); message != "" {
			narration = append(narration, message)
		}
	}

	for _, candidate := range response.Candidates {
		if candidate == nil {
			continue
		}
		if reason := explanatoryFinishReason(candidate.FinishReason); reason != "" {
			finishReasons = append(finishReasons, reason)
		}
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part == nil {
				continue
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				narration = append(narration, text)
			}
			if part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}

			// The declared mimeType is a claim; the bytes are the fact, and
			// the attachment layer keys rendering off what we store. Sniff,
			// and fall back to the declared type only to name the mismatch.
			mimeType := sniffImageMIMEType(part.InlineData.Data)
			if mimeType == "" {
				return nil, fmt.Errorf("image data declared as %q is not a recognized image format",
					strings.TrimSpace(part.InlineData.MIMEType))
			}

			images = append(images, Image{Bytes: part.InlineData.Data, MIMEType: mimeType})
		}
	}

	if len(images) == 0 {
		return nil, geminiNoImageError(narration, finishReasons)
	}
	return images, nil
}

// geminiNoImageError builds the error for a response that carried no image.
//
// This is where the native path is strictly BETTER than the managed one, and
// it is the reason it is worth having. On the managed path LiteLLM's transform
// reads only part["inlineData"] and discards finishReason, promptFeedback and
// text parts, so four different upstream causes arrive byte-identical and the
// only honest response is to name both possibilities and retry. Calling
// :generateContent directly, those signals are still here — so when the model
// DID explain itself, this reports the actual reason instead of a hedge, and
// does not spend a second billed generation rediscovering it.
//
// Retryability follows from that. An explained refusal is terminal: a content
// filter will decide the same way on the next call, so retrying only bills the
// user twice for the same "no". An UNEXPLAINED empty response is the
// intermittent upstream drop, which is exactly the case worth one retry.
// explanatoryFinishReason returns the candidate's finish reason when it
// actually explains why no image came back, and "" when it does not.
//
// Two values explain nothing and must be treated alike: an ABSENT reason, and
// FINISH_REASON_UNSPECIFIED — which is the proto enum's zero value, i.e.
// literally "the provider did not say". Reporting the latter as an explanation
// would make it terminal, so the intermittent upstream drop (the one case a
// retry reliably fixes) would fail permanently whenever the provider happened
// to send the placeholder rather than omit the field.
//
// STOP is excluded because it means the generation succeeded — if no image
// accompanied it, that is the unexplained drop, not a refusal.
//
// Every other value is passed through verbatim rather than matched against a
// known set. The SDK ships 18 finish reasons, six of them image-specific, and
// Google adds more; an allow-list would silently downgrade a brand-new refusal
// into a retry that bills the user for the same "no" twice.
func explanatoryFinishReason(reason genai.FinishReason) string {
	trimmed := strings.TrimSpace(string(reason))
	switch trimmed {
	case "", string(genai.FinishReasonStop), string(genai.FinishReasonUnspecified):
		return ""
	default:
		return trimmed
	}
}

func geminiNoImageError(narration, finishReasons []string) error {
	if len(finishReasons) == 0 && len(narration) == 0 {
		return &APIError{EmptyResult: true, Message: noImagesMessage}
	}

	message := "the model returned no image"
	if len(finishReasons) > 0 {
		message += fmt.Sprintf(" (%s)", strings.Join(finishReasons, ", "))
	}
	if len(narration) > 0 {
		message += fmt.Sprintf("; it responded with text instead: %s", strings.Join(narration, " "))
	}
	return errors.New(message)
}

// geminiAPIError normalizes the SDK's error into this package's *APIError so
// the retry ladder and every caller see one error type regardless of which
// client produced it.
//
// The SDK returns genai.APIError BY VALUE, so this unwraps to the value type
// and not a pointer.
func geminiAPIError(err error) error {
	var sdkErr genai.APIError
	if !errors.As(err, &sdkErr) {
		return fmt.Errorf("image generation request failed: %w", err)
	}

	return &APIError{
		StatusCode: sdkErr.Code,
		// AI Studio's `status` ("RESOURCE_EXHAUSTED", "INVALID_ARGUMENT") is
		// the closest analogue to OpenAI's error `type`, and it is what
		// isQuotaExhausted and any caller switching on the error can read.
		Type:    strings.TrimSpace(sdkErr.Status),
		Message: strings.TrimSpace(sdkErr.Message),
		Raw:     strings.TrimSpace(sdkErr.Error()),
	}
}
