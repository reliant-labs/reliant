// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/llm/drivers/agywire"
)

// AntigravityClient generates images against Antigravity's cloudcode endpoint.
//
// It is a THIRD client type alongside Client (OpenAI-shaped) and GeminiClient
// (AI Studio through the genai SDK), for the same reason those two are
// separate: nothing below the retry ladder is shared. The endpoint is
// v1internal:generateContent, the credential is a Google OAuth bearer rather
// than an api key, and — the part that forces a hand-rolled client — both the
// request and the response are DOUBLE-WRAPPED.
//
// That double envelope is why this cannot go through google.golang.org/genai
// even though the inner payload is ordinary Gemini. The SDK unmarshals a body
// straight into GenerateContentResponse; an Antigravity body decodes to an
// EMPTY struct, so the call returns 200, no error, and no image. See
// TestAntigravityNaiveSingleEnvelopeDecodeFindsNoImage, which pins exactly
// that silent success.
//
// What it does NOT reimplement: the wire structs come from agywire, shared with
// the chat driver; aspect-ratio mapping, MIME sniffing, the no-image error and
// the retry ladder are the same functions the Gemini client uses.
type AntigravityClient struct {
	config     Config
	httpClient *http.Client
}

// NewAntigravity builds a client for the Antigravity image surface.
//
// httpClient is supplied by the caller rather than constructed here for the
// same reason the other two clients take an SDK constructor: this package
// cannot import internal/llm (which already depends on it through llm/tools),
// and internal/llm is where the repo's sanctioned client — the one carrying a
// response-body idle timeout — is built. A nil client falls back to a plain
// one with a request timeout, which keeps tests from having to supply
// transport they do not exercise.
func NewAntigravity(config Config) (*AntigravityClient, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("antigravity image generation requires an access token")
	}
	if config.RetryBaseDelay <= 0 {
		config.RetryBaseDelay = defaultRetryBaseDelay
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	return &AntigravityClient{config: config, httpClient: httpClient}, nil
}

// ModelID returns the Reliant registry model id this client is bound to.
func (c *AntigravityClient) ModelID() string { return c.config.ModelID }

// Driver returns the provider driver id this client routes through.
func (c *AntigravityClient) Driver() string { return c.config.Driver }

// GenerateImage performs one generation call and returns the raw image bytes.
func (c *AntigravityClient) GenerateImage(ctx context.Context, request Request) (*Response, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, fmt.Errorf("image generation requires a prompt")
	}

	return generateWithRetry(ctx, c.config.ModelID, c.config.Driver, c.config.RetryBaseDelay,
		func(ctx context.Context) (*Response, error) { return c.attempt(ctx, request) })
}

// endpoint is the image surface's URL. The caller may override it through
// Config.BaseURL, which is how a test points this at an httptest server; in
// production imageGenBaseURLs supplies the captured URL.
func (c *AntigravityClient) endpoint() string {
	if baseURL := strings.TrimSpace(c.config.BaseURL); baseURL != "" {
		return baseURL
	}
	return agywire.GenerateEndpointURL
}

// buildEnvelope assembles the double-wrapped request body.
//
// Two things here are deliberately NOT the chat driver's:
//
// The model id is sent bare. Antigravity expresses reasoning effort as a
// model-id suffix ("gemini-3.8-flash-high"), but effort is a thinking concern
// and the captured image request names "gemini-3.1-flash-image" with no
// suffix. Applying one here would request a model that does not exist.
//
// generationConfig carries candidateCount and imageConfig only — no
// thinkingConfig, no maxOutputTokens — matching the capture. Quality and
// Background have no equivalent on this surface and are dropped rather than
// guessed at; Request documents its optional fields as hints that defer to the
// provider.
func (c *AntigravityClient) buildEnvelope(request Request) *agywire.RequestEnvelope {
	count := request.Count
	if count <= 0 {
		count = 1
	}

	generation := &agywire.GenerationConfig{CandidateCount: count}
	// An unrecognized size is dropped rather than forwarded: the endpoint
	// rejects an unknown aspectRatio outright, which would turn a cosmetic
	// mismatch into a failed generation, and omitting it lets the model
	// default. "auto" means "let the model decide" in both vocabularies.
	if aspectRatio := geminiAspectRatio(request.Size); aspectRatio != "" {
		generation.ImageConfig = &agywire.ImageConfig{AspectRatio: aspectRatio}
	}

	return &agywire.RequestEnvelope{
		Project:     agywire.EnvelopeProject,
		RequestID:   newImageRequestID(),
		Model:       c.config.APIModel,
		UserAgent:   agywire.EnvelopeUserAgent,
		RequestType: agywire.RequestTypeImageGen,
		Request: &agywire.GenerateReq{
			Contents:         []*agywire.Content{{Role: "user", Parts: []*agywire.Part{{Text: request.Prompt}}}},
			GenerationConfig: generation,
		},
	}
}

// newImageRequestID mirrors the capture's structured id
// (image_gen/<epochMs>/<uuid>/<n>). Whether the server parses it or treats it
// as opaque is unknown, so the shape is reproduced rather than replaced with a
// bare UUID. The trailing counter is a turn index on the chat surface; an
// image call is always a single turn, so it is fixed at 1.
func newImageRequestID() string {
	return fmt.Sprintf("image_gen/%d/%s/1", time.Now().UnixMilli(), uuid.New().String())
}

func (c *AntigravityClient) attempt(ctx context.Context, request Request) (*Response, error) {
	body, err := json.Marshal(c.buildEnvelope(request))
	if err != nil {
		return nil, fmt.Errorf("antigravity image generation: failed to marshal request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("antigravity image generation: failed to create request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.config.APIKey))
	httpRequest.Header.Set("User-Agent", agywire.UserAgentHeader)
	// Applied last so a caller-supplied header wins, which is what lets
	// imageGenConfig override the client identity without a second code path.
	for key, value := range c.config.ExtraHeaders {
		httpRequest.Header.Set(key, value)
	}

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		// Deliberately NOT an *APIError: the retry ladder only repeats those,
		// and inventing a status for a request that never reached the provider
		// would misreport what happened.
		return nil, fmt.Errorf("antigravity image generation request failed: %w", err)
	}
	defer httpResponse.Body.Close()

	// Images arrive base64-encoded inside JSON, so an unbounded read has no
	// ceiling at all. maxResponseBytes exists to stop a pathological response
	// from exhausting the worker, not to enforce policy.
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("antigravity image generation: failed to read response: %w", err)
	}

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return nil, antigravityAPIError(httpResponse, raw)
	}

	var envelope agywire.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("antigravity image generation: failed to decode response: %w", err)
	}

	images, err := decodeAntigravityImages(envelope.Response)
	if err != nil {
		return nil, err
	}

	return &Response{
		Images:   images,
		ModelID:  c.config.ModelID,
		APIModel: c.config.APIModel,
		Driver:   c.config.Driver,
		// traceId is the endpoint's own correlation id, the closest analogue
		// to the x-request-id the OpenAI-shaped path records. It travels in
		// the body rather than a header, which is why it is read here.
		UpstreamRequestID: strings.TrimSpace(envelope.TraceID),
	}, nil
}

// decodeAntigravityImages pulls image bytes out of the INNER envelope:
// response.candidates[].content.parts[].inlineData{mimeType,data}.
//
// Structurally the same job as decodeGeminiImages, and it deliberately reaches
// the same verdicts through the same helpers — geminiAspectRatio's counterpart
// on the way back is explanatoryFinishReasonString, sniffImageMIMEType and
// geminiNoImageError. Only the struct types differ, because agywire's decode
// is what survives the double wrapper.
//
// A part may carry text, or a thoughtSignature, instead of an image. Text is
// ignored when an image is present and surfaced in the error when none is,
// because in that case it is the only explanation of why nothing came back.
// The signature is dropped outright: unlike chat there is no next turn to echo
// it into.
func decodeAntigravityImages(response *agywire.GenerateResp) ([]Image, error) {
	if response == nil {
		// A body that decoded cleanly but carried no "response" member is the
		// double-envelope contract being broken, and it is indistinguishable
		// from a provider drop from here. Retryable, like every other
		// well-formed-but-empty result.
		return nil, &APIError{EmptyResult: true, Message: noImagesMessage}
	}

	var images []Image
	var narration []string
	var finishReasons []string

	if feedback := response.PromptFeedback; feedback != nil {
		if reason := strings.TrimSpace(feedback.BlockReason); reason != "" {
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
		if reason := explanatoryFinishReasonString(candidate.FinishReason); reason != "" {
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

// antigravityAPIError normalizes a non-2xx into this package's *APIError so
// the retry ladder and every caller see one error type regardless of which
// client produced it.
//
// Google's error body is NOT double-wrapped — it is a bare {"error":{…}} — so
// a body that fails to parse is kept verbatim in Raw rather than discarded.
// Status ("RESOURCE_EXHAUSTED", "INVALID_ARGUMENT") is the closest analogue to
// OpenAI's error type, and it is what isQuotaExhausted reads.
func antigravityAPIError(response *http.Response, raw []byte) error {
	apiErr := &APIError{
		StatusCode: response.StatusCode,
		Raw:        strings.TrimSpace(string(raw)),
	}

	var envelope agywire.APIErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil {
		apiErr.Type = strings.TrimSpace(envelope.Error.Status)
		apiErr.Message = strings.TrimSpace(envelope.Error.Message)
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds > 0 {
		apiErr.RetryAfter = time.Duration(seconds) * time.Second
	}
	return apiErr
}
