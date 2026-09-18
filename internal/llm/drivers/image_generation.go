// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/agywire"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/imagegen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DefaultImageGenTag is the tag an image-generation request selects by when the
// caller expresses no preference. It participates in the ordinary weighted-tag
// selector, so a user can rebind it to a concrete model in
// Settings → Model Preferences exactly like any other tag.
const DefaultImageGenTag = "image-gen"

// imageGenBaseURLs are the provider roots for drivers whose DriverConfig
// carries no explicit BaseURL. The chat drivers get these from their SDK's
// built-in default; the image path speaks raw HTTP, so it needs them named.
//
// "reliant" is absent on purpose: its base URL is resolved through
// ResolveReliantBaseURL so the request lands on the control-plane LLM proxy
// and is metered there.
//
// The codex entry is the ChatGPT backend, NOT the OpenAI platform API. They are
// different surfaces with different credentials — an OAuth bearer plus a
// chatgpt-account-id header versus a static sk- key — so they get separate
// entries even where they serve the same model name (gpt-image-2 is on both).
// CodexBaseURL already ends in /codex and the imagegen client
// appends /images/generations, composing the path verified live:
// https://chatgpt.com/backend-api/codex/images/generations
// The gemini entry is Google AI Studio's root, and it is deliberately a BARE
// root with no version suffix, unlike the others. Its client is the genai SDK,
// which appends /v1beta/models/<model>:generateContent itself; the entry exists
// so a user-configured BaseURL still overrides it through the same path every
// other driver uses.
// The antigravity entry is a COMPLETE endpoint URL, not a root, because that
// surface has no path to compose: the model travels in the request body, so
// v1internal:generateContent is the whole address. It is also NOT the chat
// URL — that one is :streamGenerateContent?alt=sse, and sending an image
// request there would ask for a stream this client does not read.
var imageGenBaseURLs = map[string]string{
	"openai":      "https://api.openai.com/v1",
	"codex":       codex.CodexBaseURL,
	"gemini":      "https://generativelanguage.googleapis.com/",
	"antigravity": agywire.GenerateEndpointURL,
}

// Codex client identity for the image endpoint, copied from the request that
// returned a verified HTTP 200. The Codex backend gates on these — an
// unrecognized originator is rejected — which is why they are sent for image
// generation exactly as the chat driver sends its own set.
//
// These are deliberately the Codex DESKTOP strings rather than the codex-tui
// strings the chat driver uses (codex/driver.go). The image endpoint is a
// Desktop-client feature and the verified call presented as Desktop; matching
// the client that is known to be served beats reusing a constant for its own
// sake.
const (
	codexImageOriginator = "Codex Desktop"
	codexImageVersion    = "0.147.0-alpha.6.5"
	codexImageUserAgent  = "Codex Desktop/0.147.0-alpha.6.5 (Mac OS 14.3.0; arm64) unknown (Codex Desktop; 26.803.41515)"
)

// ImageGenerator is what an image-generation call needs: turn a prompt into
// bytes. Declared here, at the consumer of the imagegen package, because there
// is now more than one client behind it — the OpenAI-shaped imagegen.Client
// and the native imagegen.GeminiClient — and this is the seam that lets
// ResolveImageGenerator return either one.
//
// It is structurally identical to the one-method interface the generate_image
// tool declares over the same method, and that duplication is intended: the
// tool cannot import this package (internal/llm/drivers already imports
// internal/llm/tools, so the reverse edge would be a cycle), and two local
// one-method declarations are the convention here anyway.
type ImageGenerator interface {
	GenerateImage(ctx context.Context, request imagegen.Request) (*imagegen.Response, error)
}

// ResolveImageGenerator picks an image-generation model for the user and
// returns a client bound to it.
//
// Model selection goes through the ordinary registry resolver with
// RequireOutputModality pinned to ModalityImage. That is a hard filter, unlike
// tags, so a selector like [image-gen, cheap] can never degrade into a text
// model that happened to match "cheap".
//
// Credential resolution reuses the existing driver-layer order with no new
// mechanism: the user's configured providers come from GetAvailableDrivers, and
// findBestProvider prefers a BYO provider over the managed "reliant" driver on
// a priority tie. Managed credit is therefore the fallback, not the default.
func ResolveImageGenerator(ctx context.Context, userID string, selector models.ModelSelector) (ImageGenerator, error) {
	if selector.ID == "" && len(selector.Tags) == 0 {
		selector.Tags = []string{DefaultImageGenTag}
	}
	selector.RequireOutputModality = models.ModalityImage

	availableDrivers := GetAvailableDrivers(ctx, userID)
	availableProviders := configuredProviderIDs(availableDrivers)
	if len(availableProviders) == 0 {
		return nil, fmt.Errorf("no API keys configured — add a provider key in Settings, or connect Reliant-managed credits, to generate images")
	}

	registry, err := models.GetRegistry()
	if err != nil {
		return nil, err
	}

	resolved, err := registry.Resolve(selector, availableProviders)
	if err != nil {
		return nil, fmt.Errorf("no image-generation model available: %w", err)
	}

	driverID := resolved.Provider.Driver
	driverConfig, ok := availableDrivers.Drivers[models.DriverID(driverID)]
	if !ok || !driverConfig.IsConfigured() {
		return nil, fmt.Errorf("model %s resolved to provider %s, which is not configured", resolved.Definition.ID, driverID)
	}

	config, err := imageGenConfig(resolved, driverConfig)
	if err != nil {
		return nil, err
	}

	logging.Info("Resolved image generation model",
		"model", config.ModelID,
		"api_model", config.APIModel,
		"driver", config.Driver,
		"managed", models.IsManagedDriver(models.DriverID(config.Driver)),
	)

	return newImageGenClient(config)
}

// newImageGenClient picks the client whose wire format the resolved provider
// actually speaks.
//
// "gemini" and "antigravity" are native here. The managed "reliant" driver
// reaches the same Gemini models through the control-plane LiteLLM proxy,
// which translates them into the OpenAI images shape, so managed traffic keeps
// using the OpenAI-shaped client no matter which model it resolved to. The
// branch is on the DRIVER, not the model id, for exactly that reason.
//
// Antigravity is hand-rolled HTTP rather than an SDK because its bodies are
// double-wrapped ({"response":{…}}) in both directions; the genai SDK decodes
// such a body into an empty struct, i.e. a 200 with no image and no error.
// imagegen.NewAntigravity therefore takes the http.Client directly, and it is
// the sanctioned one for the same reason the SDK constructors are injected:
// the vendor default has no idle timeout on the response body, so a provider
// that sends headers and then goes silent would hang the worker. imagegen
// cannot import internal/llm itself (that would be a cycle), so what it needs
// is passed in from here.
func newImageGenClient(config imagegen.Config) (ImageGenerator, error) {
	switch config.Driver {
	case "gemini":
		return imagegen.NewGemini(config, llm.NewGenAISDKClient)
	case antigravity.DriverID:
		if config.HTTPClient == nil {
			config.HTTPClient = llm.StreamingHTTPClient()
		}
		return imagegen.NewAntigravity(config)
	default:
		return imagegen.New(config, llm.NewOpenAISDKClient)
	}
}

// configuredProviderIDs lists the drivers the user actually has credentials
// for, which is what the registry resolver filters candidates against.
func configuredProviderIDs(availableDrivers models.AvailableDrivers) []string {
	providerIDs := make([]string, 0, len(availableDrivers.Drivers))
	for driverID, driverConfig := range availableDrivers.Drivers {
		if driverConfig.IsConfigured() {
			providerIDs = append(providerIDs, string(driverID))
		}
	}
	return providerIDs
}

// imageGenConfig turns a resolved model plus the user's credential for its
// provider into a concrete endpoint configuration.
func imageGenConfig(resolved *models.ResolvedModel, driverConfig models.DriverConfig) (imagegen.Config, error) {
	driverID := resolved.Provider.Driver

	config := imagegen.Config{
		APIKey:       driverConfig.APIKey,
		ExtraHeaders: driverConfig.ExtraHeaders,
		ModelID:      resolved.Definition.ID,
		APIModel:     resolved.Provider.APIModel,
		Driver:       driverID,
	}

	if models.IsManagedDriver(models.DriverID(driverID)) {
		// Managed credit must land on the control-plane proxy, which is the
		// only place image spend is metered. Going straight to a provider here
		// would bypass the meter entirely.
		baseURL := ResolveReliantBaseURL(driverConfig.APIKey)
		apiKey, extraHeaders := ResolveReliantAPIKey(driverConfig.APIKey, baseURL)
		config.BaseURL = baseURL
		config.APIKey = apiKey
		config.ExtraHeaders = mergeHeaders(driverConfig.ExtraHeaders, extraHeaders)
		return config, nil
	}

	baseURL := strings.TrimSpace(driverConfig.BaseURL)
	if driverID == antigravity.DriverID {
		baseURL = antigravityImageBaseURL(baseURL)
	} else if baseURL == "" {
		baseURL = imageGenBaseURLs[driverID]
	}
	if baseURL == "" {
		return imagegen.Config{}, fmt.Errorf("provider %s has no image-generation endpoint configured", driverID)
	}
	config.BaseURL = baseURL

	if driverID == "codex" {
		headers, err := codexImageHeaders(driverConfig)
		if err != nil {
			return imagegen.Config{}, err
		}
		config.ExtraHeaders = mergeHeaders(driverConfig.ExtraHeaders, headers)
	}

	return config, nil
}

// antigravityImageBaseURL resolves the endpoint for the Antigravity image
// surface.
//
// It is a special case because DriverConfig.BaseURL, for this provider, is the
// CHAT url — v1internal:streamGenerateContent?alt=sse — and honoring it would
// send an image request to a streaming endpoint. The image surface is a
// sibling path on the same host, so a configured base is respected only as a
// host override: the :generateContent path is re-derived from it.
//
// This does not exist for the other providers because none of them has one
// DriverConfig serving two endpoints with different paths.
func antigravityImageBaseURL(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" || strings.Contains(configured, ":generateContent") {
		if configured == "" {
			return imageGenBaseURLs[antigravity.DriverID]
		}
		return configured
	}
	if host, _, found := strings.Cut(configured, "/v1internal"); found {
		return host + "/v1internal:generateContent"
	}
	return imageGenBaseURLs[antigravity.DriverID]
}

// codexImageHeaders builds the non-bearer half of Codex's credential. The
// access token alone is not sufficient: the backend also needs to know which
// ChatGPT account to bill and which client is asking.
func codexImageHeaders(driverConfig models.DriverConfig) (map[string]string, error) {
	// BuildAvailableDrivers loads the account id from codex_auth_tokens into
	// AccountUUID, but that column is nullable and rows written before the id
	// was extracted have it empty. The id is a claim inside the access token
	// itself, so fall back to reading it there rather than failing a request
	// that has everything it needs.
	accountID := strings.TrimSpace(driverConfig.AccountUUID)
	if accountID == "" {
		var err error
		accountID, err = codex.AccountIDFromAccessToken(driverConfig.APIKey)
		if err != nil {
			return nil, fmt.Errorf("codex image generation requires a ChatGPT account id: %w", err)
		}
	}

	return map[string]string{
		"chatgpt-account-id": accountID,
		"originator":         codexImageOriginator,
		"version":            codexImageVersion,
		"user-agent":         codexImageUserAgent,
	}, nil
}

func mergeHeaders(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}
