// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Package imagegen is the driver-layer call path for image GENERATION. It is
// deliberately parallel to the chat path (internal/llm/drivers/*) rather than a
// mode inside it: image generation is a different endpoint
// (/v1/images/generations), a different request and response schema, and it
// does not stream. Squeezing it into llm.Driver would have forced every chat
// driver to carry a method it cannot implement.
//
// The wire format is the OpenAI images API, which is what a BYO OpenAI key,
// the Codex ChatGPT backend and the Reliant control-plane LLM proxy (LiteLLM)
// all speak. One client therefore serves all three; the only difference is the
// base URL, the credential and a set of extra headers, which the caller
// supplies via Config. See drivers.ResolveImageGenerator for how those are
// chosen.
//
// The call itself goes through the official openai-go SDK's ImageService
// rather than hand-rolled HTTP. What stays hand-written is everything the SDK
// has no opinion about or the wrong opinion about: MIME sniffed from the bytes
// rather than trusted from output_format, the url-only rejection, the
// empty-data retry capped separately because it is billed, and the managed
// quota marker the frontend keys its upgrade modal off.
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
)

const (
	// maxResponseBytes bounds the body we will buffer. Images arrive
	// base64-encoded inside JSON, so this is ~3/4 as many image bytes. The
	// attachment layer caps a stored image at 10MB (MAX_UPLOAD_SIZE), so this
	// is deliberately generous rather than tight — it exists to stop a
	// pathological response from exhausting the worker, not to enforce policy.
	maxResponseBytes = 64 << 20

	defaultRequestTimeout = 5 * time.Minute
	defaultRetryBaseDelay = 2 * time.Second
	defaultMaxAttempts    = 3

	// emptyResultMaxAttempts caps the "provider billed for an image and
	// returned none" case at ONE extra attempt, deliberately below
	// defaultMaxAttempts. See retryDelay for the cost reasoning.
	emptyResultMaxAttempts = 2
)

// noImagesMessage is what a user sees when the provider returned no image.
//
// It names BOTH possibilities because we provably cannot distinguish them on
// the managed path: LiteLLM's Vertex image transform reads only
// part["inlineData"] and returns normally when nothing matches, so a
// safety-filtered candidate, a text-only reply, a blocked prompt and a
// snake_case key all arrive here as byte-identical empty data[]. Naming one
// cause would be a guess presented as fact; naming neither leaves the user
// with no idea whether to retry or reword.
const noImagesMessage = "the provider generated an image but returned none — this is usually a transient provider issue, or the image was withheld by a content filter; retrying often succeeds"

// Config describes one credentialed image-generation endpoint. Everything in it
// is decided by the caller (drivers.ResolveImageGenerator): this package does no
// model resolution and reads no environment.
type Config struct {
	// BaseURL is the provider root including the /v1 prefix.
	BaseURL string
	// APIKey is sent as the Bearer credential.
	APIKey string
	// ExtraHeaders carries provider-specific forwarding headers — notably
	// X-Reliant-Managed-Key for managed credits against a local LiteLLM.
	ExtraHeaders map[string]string

	// ModelID is the Reliant registry model id, for logs and for the caller.
	ModelID string
	// APIModel is the provider-side model name sent on the wire.
	APIModel string
	// Driver is the provider driver id ("openai", "reliant", …).
	Driver string

	// HTTPClient is optional; nil means the one the injected SDK constructor
	// installs, which is what production wants — llm.NewOpenAISDKClient's
	// client carries the response-body idle timeout.
	HTTPClient *http.Client
	// RetryBaseDelay is optional; zero means defaultRetryBaseDelay.
	RetryBaseDelay time.Duration
}

// Client generates images against one configured endpoint.
//
// Returned as a concrete type on purpose. Consumers that need substitution
// (the generate_image tool, its tests) declare their own one-method interface
// locally over GenerateImage rather than depending on an interface exported
// from here.
type Client struct {
	config Config
	images openAIImages
}

// openAIImages is the one thing this client needs from the openai-go SDK.
// Declared at the consumer so the SDK can be faked in tests without a network
// round trip, and so this file states its dependency as one method rather than
// the whole openaisdk.Client surface.
type openAIImages interface {
	Generate(ctx context.Context, body openaisdk.ImageGenerateParams, opts ...openaiopt.RequestOption) (*openaisdk.ImagesResponse, error)
}

// OpenAIClientFactory builds the openai-go client this package calls through.
//
// It is a parameter rather than a direct openaisdk.NewClient call for the same
// reason GenAIClientFactory is: the vendor constructor defaults to an
// http.Client with no idle timeout on the response body, and this repo funnels
// every SDK client through llm.NewOpenAISDKClient to install one. That rule is
// enforced statically by TestDriversUseSanctionedSDKConstructors, and this
// package cannot import internal/llm to satisfy it — internal/llm already
// depends on this package (through llm/tools), so the edge back would be a
// cycle.
//
// So the caller supplies the constructor. drivers.newImageGenClient passes
// llm.NewOpenAISDKClient, which is exactly the sanctioned one.
type OpenAIClientFactory func(opts ...openaiopt.RequestOption) openaisdk.Client

// New builds a client for the given endpoint using the supplied SDK
// constructor.
//
// This uses the github.com/openai/openai-go/v3 SDK rather than hand-rolled
// HTTP. The SDK is already a direct dependency (the openai, codex, azure,
// local and reliant chat drivers all use it) and its ImageService posts to
// exactly the path this client composed by hand: {base}/images/generations.
//
// One SDK-backed client serves all three OpenAI-shaped drivers — openai,
// codex and the managed reliant proxy — because option.WithBaseURL and
// option.WithHeader reproduce every requirement each of them has. In
// particular Codex's Desktop identity headers, including its override of the
// SDK's own User-Agent, are carried verbatim.
func New(config Config, newClient OpenAIClientFactory) (*Client, error) {
	if newClient == nil {
		return nil, fmt.Errorf("image generation requires an SDK client constructor")
	}
	if config.RetryBaseDelay <= 0 {
		config.RetryBaseDelay = defaultRetryBaseDelay
	}

	sdkClient := newClient(clientOptions(config)...)
	return &Client{config: config, images: &sdkClient.Images}, nil
}

// newWithImages builds a client over an arbitrary image caller. Used by tests
// to substitute the SDK without a network round trip.
func newWithImages(config Config, images openAIImages) *Client {
	if config.RetryBaseDelay <= 0 {
		config.RetryBaseDelay = defaultRetryBaseDelay
	}
	return &Client{config: config, images: images}
}

// clientOptions maps a Config onto the SDK's request options.
//
// The two that are not obvious, and that a future edit must not drop:
//
// MaxRetries is pinned to 0 because this package runs its OWN retry ladder
// (see retry.go), which distinguishes cases the SDK cannot. The SDK retries
// every 429 — including managed-credit exhaustion, which is hard-capped and
// will never succeed — and knows nothing about the empty-data case, which is
// billed and therefore capped at one extra attempt. Leaving the SDK default of
// 2 would multiply with our ladder for up to nine billed calls.
//
// The response body is bounded by a middleware rather than by the SDK, which
// reads it with an unbounded io.ReadAll. Images arrive base64-encoded inside
// JSON, so a pathological response could otherwise exhaust the worker.
func clientOptions(config Config) []openaiopt.RequestOption {
	options := []openaiopt.RequestOption{
		openaiopt.WithBaseURL(strings.TrimSpace(config.BaseURL)),
		openaiopt.WithMaxRetries(0),
		openaiopt.WithRequestTimeout(defaultRequestTimeout),
		openaiopt.WithMiddleware(limitResponseBody, tolerateMislabeledJSON),
	}
	if config.HTTPClient != nil {
		options = append(options, openaiopt.WithHTTPClient(config.HTTPClient))
	}
	if apiKey := strings.TrimSpace(config.APIKey); apiKey != "" {
		options = append(options, openaiopt.WithAPIKey(apiKey))
	}
	// Applied last so a caller-supplied header wins over anything the SDK set
	// by default. Codex depends on this: it replaces the SDK's own User-Agent
	// with the Codex Desktop string the backend gates on.
	for key, value := range config.ExtraHeaders {
		options = append(options, openaiopt.WithHeader(key, value))
	}
	return options
}

// limitResponseBody caps how much of a response this package will buffer. The
// SDK reads the body with an unbounded io.ReadAll, so without this a
// pathological response has no ceiling at all.
func limitResponseBody(request *http.Request, next openaiopt.MiddlewareNext) (*http.Response, error) {
	response, err := next(request)
	if response != nil && response.Body != nil {
		response.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.LimitReader(response.Body, maxResponseBytes), Closer: response.Body}
	}
	return response, err
}

// tolerateMislabeledJSON accepts a success body that is JSON but was not
// labelled as such.
//
// The SDK hard-fails a 2xx whose Content-Type is not application/json —
// "expected destination type of 'string' or '[]byte'" — before it ever looks at
// the bytes. That strictness is wrong for this path: we speak to three
// different fronts (OpenAI, the Codex ChatGPT backend, and a LiteLLM proxy),
// and a gateway that omits the header, or lets Go sniff it to text/plain,
// would turn a perfectly good image into an unexplainable decode failure.
//
// Only a 2xx is relabelled, and only when the body actually begins with a JSON
// object. A non-JSON success body still fails, which is the correct outcome —
// this widens what we accept, it does not stop checking.
func tolerateMislabeledJSON(request *http.Request, next openaiopt.MiddlewareNext) (*http.Response, error) {
	response, err := next(request)
	if err != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	if mediaType, _, _ := strings.Cut(response.Header.Get("Content-Type"), ";"); strings.TrimSpace(mediaType) == "application/json" {
		return response, nil
	}

	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("failed to read image generation response: %w", readErr)
	}
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		response.Header.Set("Content-Type", "application/json")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

// ModelID returns the Reliant registry model id this client is bound to.
func (c *Client) ModelID() string { return c.config.ModelID }

// Driver returns the provider driver id this client routes through.
func (c *Client) Driver() string { return c.config.Driver }

// Request is one image-generation call. Only Prompt is required; every other
// field left at its zero value defers to the provider's default.
type Request struct {
	Prompt string

	// Size is a provider size string such as "1024x1024" or "auto".
	Size string
	// Count is how many images to generate. Zero means one.
	Count int
	// Quality is a provider quality hint ("low", "medium", "high", "auto").
	Quality string
	// Background is a provider background hint ("transparent", "opaque", "auto").
	Background string
	// OutputFormat requests an encoding ("png", "jpeg", "webp"). The MIME type
	// on the result is sniffed from the returned bytes regardless, so this is a
	// request, not a promise.
	OutputFormat string
}

// Image is one generated image as raw bytes plus the MIME type sniffed from
// those bytes.
//
// Bytes, not a base64 string, and not a provider struct: the consumer persists
// this as an Attachment row and hands it to a model as message.BinaryContent,
// and both of those want bytes. Re-encoding to base64 here would force every
// consumer to decode it again.
type Image struct {
	Bytes    []byte
	MIMEType string
	// RevisedPrompt is the provider's rewritten prompt when it reports one.
	RevisedPrompt string
}

// Response is the result of one generation call.
type Response struct {
	Images []Image

	ModelID  string
	APIModel string
	Driver   string

	// UpstreamRequestID and LiteLLMCallID are correlation identifiers copied
	// from response headers, matching what the chat drivers record. The
	// LiteLLM call id is also the key the control-plane meter dedupes spend
	// by, so it is the join between a generated image and its billed cost.
	UpstreamRequestID string
	LiteLLMCallID     string
}

// APIError is a non-2xx response from the image endpoint, with the provider's
// OpenAI-shaped error envelope parsed out when present.
type APIError struct {
	StatusCode int
	Type       string
	Code       string
	Message    string
	Raw        string
	// RetryAfter is the provider's Retry-After hint when it sent one. Zero
	// means the backoff ladder decides.
	RetryAfter time.Duration

	// EmptyResult marks the one failure that is NOT an HTTP failure: the call
	// returned 200 and a well-formed envelope carrying no image. StatusCode is
	// 0 in that case, because inventing a 5xx would misreport what the
	// provider actually said.
	//
	// It is retryable — see emptyResultMaxAttempts for why it is capped
	// separately from the transport ladder.
	EmptyResult bool
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = strings.TrimSpace(e.Raw)
	}
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	// An empty result has no status to report; saying "status 0" would read as
	// a transport failure, which is precisely what it is not.
	if e.EmptyResult {
		return message
	}
	return fmt.Sprintf("image generation failed (status %d): %s", e.StatusCode, message)
}

// GenerateImage performs one non-streaming generation call and returns the raw
// image bytes.
func (c *Client) GenerateImage(ctx context.Context, request Request) (*Response, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, fmt.Errorf("image generation requires a prompt")
	}

	return generateWithRetry(ctx, c.config.ModelID, c.config.Driver, c.config.RetryBaseDelay,
		func(ctx context.Context) (*Response, error) { return c.attempt(ctx, request) })
}

// generateParams maps a Request onto the SDK's ImageGenerateParams. Zero-valued
// optional fields are left unset so the SDK's omitzero tags drop them from the
// body and the provider applies its own default rather than receiving an empty
// string it will reject.
//
// Size, Quality, Background and OutputFormat are named enum types in the SDK
// but they are plain strings underneath and are serialized without validation
// (verified against a live-shaped request), so a provider-specific value that
// has no SDK constant — an arbitrary WIDTHxHEIGHT that gpt-image-2 accepts, for
// one — still reaches the wire. Converting rather than switching on the known
// constants is deliberate: an allow-list here would silently drop a size the
// provider supports and we do not yet know about.
func (c *Client) generateParams(request Request) openaisdk.ImageGenerateParams {
	count := request.Count
	if count <= 0 {
		count = 1
	}

	params := openaisdk.ImageGenerateParams{
		Prompt: request.Prompt,
		Model:  openaisdk.ImageModel(c.config.APIModel),
		N:      openaisdk.Int(int64(count)),
	}
	if request.Size != "" {
		params.Size = openaisdk.ImageGenerateParamsSize(request.Size)
	}
	if request.Quality != "" {
		params.Quality = openaisdk.ImageGenerateParamsQuality(request.Quality)
	}
	if request.Background != "" {
		params.Background = openaisdk.ImageGenerateParamsBackground(request.Background)
	}
	if request.OutputFormat != "" {
		params.OutputFormat = openaisdk.ImageGenerateParamsOutputFormat(request.OutputFormat)
	}
	return params
}

func (c *Client) attempt(ctx context.Context, request Request) (*Response, error) {
	// The SDK decodes into its own typed envelope AND hands back the raw
	// *http.Response, which is how the correlation headers below survive the
	// port — they are not fields on ImagesResponse.
	var httpResponse *http.Response
	response, err := c.images.Generate(ctx, c.generateParams(request),
		openaiopt.WithResponseInto(&httpResponse))
	if err != nil {
		return nil, c.apiError(err)
	}

	images, err := decodeImages(response)
	if err != nil {
		return nil, err
	}

	result := &Response{
		Images:   images,
		ModelID:  c.config.ModelID,
		APIModel: c.config.APIModel,
		Driver:   c.config.Driver,
	}
	if httpResponse != nil {
		result.UpstreamRequestID = strings.TrimSpace(httpResponse.Header.Get("x-request-id"))
		result.LiteLLMCallID = strings.TrimSpace(httpResponse.Header.Get("x-litellm-call-id"))
	}
	return result, nil
}

func decodeImages(response *openaisdk.ImagesResponse) ([]Image, error) {
	if response == nil || len(response.Data) == 0 {
		// Retryable, not terminal. A 200 with empty data[] is an intermittent
		// upstream drop — Vertex occasionally returns a candidate with no
		// image part while still billing the generation — and the same prompt
		// succeeded on 3 of 3 live retries.
		return nil, &APIError{EmptyResult: true, Message: noImagesMessage}
	}

	images := make([]Image, 0, len(response.Data))
	for i, entry := range response.Data {
		if strings.TrimSpace(entry.B64JSON) == "" {
			// A url-only response cannot satisfy this call's contract (raw
			// bytes), and silently fetching it would issue a second,
			// unmetered request from the worker. Name the case instead.
			if strings.TrimSpace(entry.URL) != "" {
				return nil, fmt.Errorf("image %d returned a url instead of inline data; this model must be configured to return b64_json", i)
			}
			return nil, fmt.Errorf("image %d contained no data", i)
		}

		decoded, err := base64.StdEncoding.DecodeString(entry.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("failed to decode image %d: %w", i, err)
		}

		mimeType := sniffImageMIMEType(decoded)
		if mimeType == "" {
			return nil, fmt.Errorf("image %d is not a recognized image format", i)
		}

		images = append(images, Image{
			Bytes:         decoded,
			MIMEType:      mimeType,
			RevisedPrompt: strings.TrimSpace(entry.RevisedPrompt),
		})
	}
	return images, nil
}

// sniffImageMIMEType derives the MIME type from the bytes themselves rather
// than trusting the requested output_format: the provider may ignore the
// request, and the attachment layer keys rendering off the stored MIME type.
// Returns "" when the bytes are not an image.
func sniffImageMIMEType(data []byte) string {
	detected := http.DetectContentType(data)
	if mediaType, _, ok := strings.Cut(detected, ";"); ok {
		detected = mediaType
	}
	detected = strings.TrimSpace(detected)
	if !strings.HasPrefix(detected, "image/") {
		return ""
	}
	return detected
}

// apiError normalizes the SDK's error into this package's *APIError so the
// retry ladder and every caller see one error type regardless of how the call
// failed.
//
// The SDK returns *openai.Error for any HTTP >= 400 and the bare transport
// error otherwise, so a connection failure is deliberately NOT turned into an
// *APIError: retryDelay only repeats an *APIError, and inventing a status for
// a request that never reached the provider would misreport what happened.
//
// Note the SDK parses only the "error" SUBOBJECT into its fields — its
// RawJSON() is that subobject, not the whole body. isQuotaExhausted greps Raw
// for a quoted marker, and upgrade_url lives inside the same subobject, so
// this reads both from RawJSON and the contract holds.
func (c *Client) apiError(err error) error {
	var sdkErr *openaisdk.Error
	if !errors.As(err, &sdkErr) {
		return fmt.Errorf("image generation request failed: %w", err)
	}

	apiErr := &APIError{
		StatusCode: sdkErr.StatusCode,
		Type:       strings.TrimSpace(sdkErr.Type),
		Code:       strings.TrimSpace(sdkErr.Code),
		Message:    strings.TrimSpace(sdkErr.Message),
		Raw:        strings.TrimSpace(sdkErr.RawJSON()),
	}
	if sdkErr.Response != nil {
		apiErr.RetryAfter, _ = parseRetryAfterSeconds(sdkErr.Response.Header.Get("Retry-After"))
	}

	// Managed-credit exhaustion is terminal and needs to survive Temporal's
	// stringification of activity errors so the frontend can open the upgrade
	// modal — the same contract the chat path uses. Only the managed driver
	// spends Reliant credit; a BYO key's quota error is the user's own billing
	// relationship and is passed through untouched.
	if sdkErr.StatusCode == http.StatusTooManyRequests && c.config.Driver == managedDriverID && isQuotaExhausted(apiErr) {
		upgradeURL := upgradeURLFrom(apiErr.Raw)
		if upgradeURL == "" {
			upgradeURL = defaultUpgradeURL
		}
		message := apiErr.Message
		if message == "" {
			message = "reliant-managed LLM quota exhausted"
		}
		return fmt.Errorf("%s", chatmarkers.Wrap(chatmarkers.KindReliantManagedQuotaExhausted, upgradeURL, message))
	}

	return apiErr
}

const (
	// managedDriverID mirrors models.ManagedDriverID. It is duplicated as a
	// local string rather than imported so this package depends only on the
	// wire format and not on the model registry.
	managedDriverID   = "reliant"
	defaultUpgradeURL = "/billing/plans"
)

// upgradeURLFrom pulls upgrade_url out of the provider's error subobject.
//
// The SDK's typed *openai.Error has no field for it — it is a control-plane
// extension, not part of the OpenAI schema — but it does preserve the raw JSON
// it parsed, so the value is still recoverable. A malformed body simply yields
// "" and the caller falls back to defaultUpgradeURL.
func upgradeURLFrom(raw string) string {
	var envelope struct {
		UpgradeURL string `json:"upgrade_url"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.UpgradeURL)
}

func isQuotaExhausted(apiErr *APIError) bool {
	if strings.EqualFold(apiErr.Code, "insufficient_quota") || strings.EqualFold(apiErr.Type, "insufficient_quota") {
		return true
	}
	// AI Studio's spelling of the same condition. Its daily per-project quota
	// does not clear within a backoff ladder, so treating it as transient
	// would spend two more retries to arrive at the same error.
	if strings.EqualFold(apiErr.Type, "RESOURCE_EXHAUSTED") {
		return true
	}
	return strings.Contains(strings.ToLower(apiErr.Raw), `"insufficient_quota"`)
}

// parseRetryAfterSeconds is kept separate so a provider-supplied Retry-After can
// be honored without duplicating the header lookup at each call site.
func parseRetryAfterSeconds(value string) (time.Duration, bool) {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}
