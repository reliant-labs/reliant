// Copyright (c) 2025 Reliant Labs
package local

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Family constant for Local
const Family models.Family = "local"

const (
	ProviderLocal models.Family = "local"

	localModelsPath        = "v1/models"
	lmStudioBetaModelsPath = "api/v0/models"
)

var (
	// globalLocalConfig stores the local provider configuration for runtime access
	globalLocalConfig *models.LocalProviderConfig
	localConfigMu     sync.RWMutex
)

func init() {
	// Register the local driver factory
	// Note: Unlike other drivers, local models are discovered dynamically at startup,
	// not statically defined. The driver factory is registered here, and models are
	// registered when DiscoverAndRegisterModels is called.
	registry.RegisterDriver(Family, createClient)
}

// SetLocalConfig stores the local provider configuration for runtime access.
// This should be called during application startup after loading the config.
func SetLocalConfig(cfg *models.LocalProviderConfig) {
	localConfigMu.Lock()
	defer localConfigMu.Unlock()
	globalLocalConfig = cfg
}

// GetLocalConfig returns the stored local provider configuration.
// Returns nil if no local provider is configured.
func GetLocalConfig() *models.LocalProviderConfig {
	localConfigMu.RLock()
	defer localConfigMu.RUnlock()
	return globalLocalConfig
}

// DiscoverAndRegisterModels discovers models from a local endpoint and registers them.
// This should be called after model discovery to register the driver-model mapping.
func DiscoverAndRegisterModels(baseURL string) error {
	definitions, err := DiscoverModels(baseURL)
	if err != nil {
		return err
	}

	// Register each discovered model with the driver
	var modelIDs []models.ModelID
	for _, def := range definitions {
		modelIDs = append(modelIDs, models.ModelID(def.ID))
	}

	if len(modelIDs) > 0 {
		models.RegisterDriverModels(Family, modelIDs)
		logging.Info("Registered local models with driver",
			"count", len(modelIDs),
			"models", modelIDs)
	}

	return nil
}

// DiscoverModels queries a local model server and returns discovered models as ModelDefinitions.
// The baseURL should be the OpenAI-compatible API endpoint (e.g., http://localhost:11434/v1).
// This function tries the LM Studio beta API first, then falls back to the standard v1/models endpoint.
func DiscoverModels(baseURL string) ([]models.ModelDefinition, error) {
	localEndpoint, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse local endpoint: %w", err)
	}

	load := func(u *url.URL, path string) []localModel {
		u.Path = path
		return listLocalModels(u.String())
	}

	// Try LM Studio beta API first, then standard OpenAI-compatible endpoint
	localModels := load(localEndpoint, lmStudioBetaModelsPath)
	if len(localModels) == 0 {
		localModels = load(localEndpoint, localModelsPath)
	}

	if len(localModels) == 0 {
		logging.Debug("No local models found",
			"endpoint", baseURL,
		)
		return nil, nil // No models found, but not an error
	}

	return convertToDefinitions(localModels), nil
}

// convertToDefinitions converts local models to ModelDefinitions for the registry.
func convertToDefinitions(localModels []localModel) []models.ModelDefinition {
	var definitions []models.ModelDefinition
	for _, m := range localModels {
		def := convertLocalModelToDefinition(m)
		definitions = append(definitions, def)
	}
	return definitions
}

type localModelList struct {
	Data []localModel `json:"data"`
}

type localModel struct {
	ID                  string `json:"id"`
	Object              string `json:"object"`
	Type                string `json:"type"`
	Publisher           string `json:"publisher"`
	Arch                string `json:"arch"`
	CompatibilityType   string `json:"compatibility_type"`
	Quantization        string `json:"quantization"`
	State               string `json:"state"`
	MaxContextLength    int64  `json:"max_context_length"`
	LoadedContextLength int64  `json:"loaded_context_length"`
}

func listLocalModels(modelsEndpoint string) []localModel {
	res, err := http.Get(modelsEndpoint) //nolint:gosec // G107: Variable URL is intentional for user-configured model servers
	if err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
		return []localModel{}
	}
	defer func() {
		_ = res.Body.Close() // Best effort close, not critical for reading
	}()

	if res.StatusCode != http.StatusOK {
		logging.Debug("Failed to list local models",
			"status", res.StatusCode,
			"endpoint", modelsEndpoint,
		)
		return []localModel{}
	}

	var modelList localModelList
	if err = json.NewDecoder(res.Body).Decode(&modelList); err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
		return []localModel{}
	}

	var supportedModels []localModel
	for _, model := range modelList.Data {
		if strings.HasSuffix(modelsEndpoint, lmStudioBetaModelsPath) {
			if model.Object != "model" || model.Type != "llm" {
				logging.Debug("Skipping unsupported LMStudio model",
					"endpoint", modelsEndpoint,
					"id", model.ID,
					"object", model.Object,
					"type", model.Type,
				)

				continue
			}
		}

		supportedModels = append(supportedModels, model)
	}

	return supportedModels
}

// DefaultContextWindow is the fallback context window size when the local model
// server doesn't report one. Set high (200k) since most modern local models
// support large contexts and Reliant's system prompts + tools are ~20k tokens.
const DefaultContextWindow = 200000

// convertLocalModelToDefinition converts a localModel to a ModelDefinition.
func convertLocalModelToDefinition(m localModel) models.ModelDefinition {
	contextWindow := m.LoadedContextLength
	if contextWindow == 0 {
		contextWindow = m.MaxContextLength
	}
	if contextWindow == 0 {
		contextWindow = DefaultContextWindow
	}

	return models.ModelDefinition{
		ID:         "local-" + sanitizeModelID(m.ID),
		Name:       friendlyModelName(m.ID),
		Tags:       []string{"local"},
		Visibility: models.VisibilityUser,
		Capabilities: models.ModelCapabilities{
			MaxContextWindow:    int(contextWindow),
			MaxOutputTokens:     int(contextWindow),
			SupportsTools:       true,
			SupportsStreaming:   true,
			SupportsAttachments: true,
		},
		Providers: []models.ProviderMapping{
			{
				Driver:   "local",
				APIModel: m.ID,
			},
		},
	}
}

// sanitizeModelID converts a local model ID to a valid registry ID.
// Replaces characters that might cause issues with hyphens.
func sanitizeModelID(id string) string {
	// Replace colons, slashes, and other special chars with hyphens
	id = strings.ReplaceAll(id, ":", "-")
	id = strings.ReplaceAll(id, "/", "-")
	id = strings.ReplaceAll(id, "@", "-")
	id = strings.ReplaceAll(id, " ", "-")
	// Remove consecutive hyphens
	for strings.Contains(id, "--") {
		id = strings.ReplaceAll(id, "--", "-")
	}
	// Trim leading/trailing hyphens
	id = strings.Trim(id, "-")
	return strings.ToLower(id)
}

var modelInfoRegex = regexp.MustCompile(`(?i)^([a-z0-9]+)(?:[-_]?([rv]?\d[\.\d]*))?(?:[-_]?([a-z]+))?.*`)

func friendlyModelName(modelID string) string {
	mainID := modelID
	tag := ""

	if slash := strings.LastIndex(mainID, "/"); slash != -1 {
		mainID = mainID[slash+1:]
	}

	if at := strings.Index(modelID, "@"); at != -1 {
		mainID = modelID[:at]
		tag = modelID[at+1:]
	}

	match := modelInfoRegex.FindStringSubmatch(mainID)
	if match == nil {
		return modelID
	}

	capitalize := func(s string) string {
		if s == "" {
			return ""
		}
		runes := []rune(s)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}

	family := capitalize(match[1])
	version := ""
	label := ""

	if len(match) > 2 && match[2] != "" {
		version = strings.ToUpper(match[2])
	}

	if len(match) > 3 && match[3] != "" {
		label = capitalize(match[3])
	}

	var parts []string
	if family != "" {
		parts = append(parts, family)
	}
	if version != "" {
		parts = append(parts, version)
	}
	if label != "" {
		parts = append(parts, label)
	}
	if tag != "" {
		parts = append(parts, tag)
	}

	return strings.Join(parts, " ")
}
