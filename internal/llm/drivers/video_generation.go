// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/videogen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DefaultVideoGenTag is the tag a video-generation request selects by when the
// caller expresses no preference.
const DefaultVideoGenTag = "video-gen"

// videoGenBaseURLs are provider roots for drivers whose DriverConfig carries no
// explicit BaseURL. "reliant" is absent: its base URL is the control-plane
// proxy, resolved by ResolveReliantBaseURL.
var videoGenBaseURLs = map[string]string{
	"gemini": "https://generativelanguage.googleapis.com/",
}

// VideoGenerator is the client a video-generation call needs. Declared here, at
// the consumer of the videogen package; the generate_video tool declares its
// own equivalent for the same cycle reason as ImageGenerator.
type VideoGenerator interface {
	Info() videogen.ModelInfo
	Submit(ctx context.Context, request videogen.Request) (videogen.Job, error)
	Poll(ctx context.Context, job videogen.Job) (*videogen.Response, bool, error)
	Cancel(ctx context.Context, job videogen.Job) error
}

// ResolveVideoGenerator picks a video-generation model for the user and returns
// a client bound to it.
//
// Selection goes through the ordinary registry resolver with
// RequireOutputModality pinned to ModalityVideo, a hard filter, so a selector
// like [video-gen, flagship] can never degrade onto a text model that happens
// to be tagged flagship.
func ResolveVideoGenerator(ctx context.Context, userID string, selector models.ModelSelector) (VideoGenerator, error) {
	if selector.ID == "" && len(selector.Tags) == 0 {
		selector.Tags = []string{DefaultVideoGenTag}
	}

	resolved, availableDrivers, err := resolveMediaModel(ctx, userID, selector, models.ModalityVideo)
	if err != nil {
		return nil, err
	}

	driverID := resolved.Provider.Driver
	driverConfig, ok := availableDrivers.Drivers[models.DriverID(driverID)]
	if !ok || !driverConfig.IsConfigured() {
		return nil, fmt.Errorf("model %s resolved to provider %s, which is not configured", resolved.Definition.ID, driverID)
	}

	config := videogen.Config{
		APIKey:       driverConfig.APIKey,
		ExtraHeaders: driverConfig.ExtraHeaders,
		ModelID:      resolved.Definition.ID,
		APIModel:     resolved.Provider.APIModel,
		Driver:       driverID,
		Capabilities: resolved.Definition.Capabilities.Video,
	}
	baseURL := strings.TrimSpace(driverConfig.BaseURL)
	if models.IsManagedDriver(models.DriverID(driverID)) {
		// Managed video must land on the control-plane proxy, the only metered path.
		baseURL = ResolveReliantBaseURL(driverConfig.APIKey)
		apiKey, extraHeaders := ResolveReliantAPIKey(driverConfig.APIKey, baseURL)
		config.APIKey = apiKey
		config.ExtraHeaders = mergeHeaders(driverConfig.ExtraHeaders, extraHeaders)
	} else if baseURL == "" {
		baseURL = videoGenBaseURLs[driverID]
	}
	config.BaseURL = baseURL

	logging.Debug("Resolved video generation model",
		"model", config.ModelID, "api_model", config.APIModel, "driver", config.Driver)

	return newVideoGenClient(config)
}

// newVideoGenClient picks the client whose wire format the resolved provider
// speaks. The branch is on the model family, because the gemini driver serves
// both Veo (predictLongRunning) and, later, Omni (Interactions).
func newVideoGenClient(config videogen.Config) (VideoGenerator, error) {
	switch config.Driver {
	case "gemini":
		if strings.HasPrefix(config.APIModel, "gemini-omni") {
			return videogen.NewOmni(config, llm.NewGenAISDKClient)
		}
		return videogen.NewVeo(config, llm.NewGenAISDKClient)
	case "reliant":
		return videogen.NewManaged(config)
	default:
		return nil, fmt.Errorf("provider %s has no video-generation client", config.Driver)
	}
}
