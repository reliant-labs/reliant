// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/drivers/replay"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"

	// Import drivers to trigger their init() functions for registry registration
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/gemini"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/local"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/openrouter"
	_ "github.com/reliant-labs/reliant/internal/llm/drivers/vertexai"
)

var ErrUnsupportedFamily = errors.New("unsupported model family")
var MockAllowed = false
var Override llm.Driver

// baseDriver provides base implementation for drivers
type baseDriver struct {
	model  models.Model
	client registry.Client
}

func (p *baseDriver) Name() string {
	return p.client.Name()
}

// SendMessages implements the Driver interface
func (p *baseDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	return p.client.SendMessages(ctx, prompts, messages, tools)
}

// StreamResponse implements the Driver interface
func (p *baseDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent)
	go func() {
		defer close(ch)
		for event := range p.client.StreamResponse(ctx, prompts, messages, tools) {
			// Enrich the event with the model used
			event.Model = p.model
			ch <- event
		}
	}()
	return ch
}

// Model implements the Driver interface
func (p *baseDriver) Model() models.Model {
	return p.model
}

// ValidateKey implements the Driver interface
func (p *baseDriver) ValidateKey(ctx context.Context) error {
	return p.client.ValidateKey(ctx)
}

// GetDriverForModel creates and returns a driver for the specified model
// This function determines which driver can handle the model
func GetDriverForModel(model models.Model, driverID models.Family, opts ...llm.DriverOption) (llm.Driver, error) {
	options := llm.DriverOptions{}
	for _, o := range opts {
		o(&options)
	}
	options.Model = model

	// Check override first (for testing) before validating model
	if Override != nil {
		return Override, nil
	}

	// Verify this driver supports the model
	if !models.CanDriverUseModel(driverID, model.ID) {
		return nil, fmt.Errorf("driver %s does not support model: %s", driverID, model.ID)
	}

	// Look up the driver factory from the registry
	factory, ok := registry.GetDriverFactory(driverID)
	if !ok {
		return nil, fmt.Errorf("provider %s is not supported (not registered)", driverID)
	}

	// Create the client using the registered factory
	driverClient, err := factory(&options)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s client: %w", driverID, err)
	}

	return &baseDriver{client: driverClient, model: options.Model}, nil
}

// GetDriver allows injecting a custom driver function for testing
var GetDriver func(ctx context.Context, userID string, preferences models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) = defaultGetDriver

// ReplayMode holds the current replay configuration
var ReplayMode struct {
	Enabled  bool
	FilePath string
	Driver   llm.Driver
}

// SetReplayMode configures the resolver to use replay mode
func SetReplayMode(filePath string) error {
	if filePath == "" {
		ReplayMode.Enabled = false
		ReplayMode.Driver = nil
		return nil
	}

	// Always use the comprehensive driver - it handles single sessions too
	replayDriver, err := replay.NewComprehensiveReplayDriver(filePath)
	if err != nil {
		return fmt.Errorf("failed to create replay driver: %w", err)
	}

	ReplayMode.Enabled = true
	ReplayMode.FilePath = filePath
	ReplayMode.Driver = replayDriver

	return nil
}

// ParseModelIDWithDriver extracts the base model ID and optional driver from a combined model ID
// Format: "model-id" or "model-id@driver" (e.g., "claude-4.5-sonnet@openrouter")
// Returns: baseModelID, driverID (empty string if no driver specified)
func ParseModelIDWithDriver(modelID string) (string, string) {
	if idx := strings.LastIndex(modelID, "@"); idx != -1 {
		return modelID[:idx], modelID[idx+1:]
	}
	return modelID, ""
}

// defaultGetDriver tries to get a driver using the provided model preferences
func defaultGetDriver(ctx context.Context, userID string, preferences models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
	// Check if we have an override driver set (for testing)
	if Override != nil {
		return Override, nil
	}

	// Check if we're in replay mode
	if ReplayMode.Enabled && ReplayMode.Driver != nil {
		return ReplayMode.Driver, nil
	}
	logging.Debug("calling defaultGetDriver", "preferences", preferences)
	for _, pref := range preferences {
		// Parse model ID to extract base model and optional explicit driver
		// Format: "model-id" or "model-id@driver" (e.g., "claude-4.5-sonnet@openrouter")
		baseModelID, explicitDriverID := ParseModelIDWithDriver(string(pref.ModelID))
		modelID := models.ModelID(baseModelID)

		logging.Debug("parsing model preference", "original", pref.ModelID, "baseModel", baseModelID, "explicitDriver", explicitDriverID)

		// Get model details from registry
		registry := models.MustGetRegistry()
		def, ok := registry.GetDefinition(string(modelID))
		if !ok {
			logging.Debug("model not found", "modelID", modelID)
			continue
		}
		model := def.ToModel()

		// Use preference's token budget if specified, otherwise model's default
		maxTokens := int64(def.Capabilities.MaxOutputTokens)
		if pref.TokenBudget != nil {
			maxTokens = *pref.TokenBudget
		}

		// Get available drivers
		availableDrivers := GetAvailableDrivers(ctx, userID)

		var driverConfig models.DriverConfig
		var found bool

		if explicitDriverID != "" {
			// User explicitly selected a driver (e.g., "openrouter" or "local")
			// Use that driver directly instead of auto-selecting
			config, exists := availableDrivers.Drivers[models.DriverID(explicitDriverID)]
			if exists && config.IsConfigured() {
				// Verify this driver supports the model
				if models.CanDriverUseModel(models.Family(explicitDriverID), modelID) {
					driverConfig = config
					found = true
					logging.Debug("using explicit driver", "driver", explicitDriverID, "model", modelID)
				} else {
					logging.Warn("explicit driver does not support model", "driver", explicitDriverID, "model", modelID)
				}
			} else {
				logging.Warn("explicit driver not available", "driver", explicitDriverID, "exists", exists, "isConfigured", config.IsConfigured())
			}
		}

		// Fall back to auto-selection if no explicit driver or explicit driver wasn't available
		if !found {
			driverConfig, found = models.SelectBestDriver(model.ID, availableDrivers)
			logging.Debug("auto-selected driver", "driverConfig", driverConfig, "found", found)
		}

		if !found {
			continue // Try next model in preferences
		}

		// Build driver options with the API key from the selected driver
		driverOpts := []llm.DriverOption{
			llm.WithAPIKey(driverConfig.APIKey),
			llm.WithModel(model),
			llm.WithMaxTokens(maxTokens),
		}

		// Add base URL if specified for this driver
		if driverConfig.BaseURL != "" {
			driverOpts = append(driverOpts, llm.WithBaseURL(driverConfig.BaseURL))
		}

		// Pass Claude OAuth account metadata and token refresh if available
		if driverConfig.AccountUUID != "" {
			driverOpts = append(driverOpts, llm.WithAccountMetadata(driverConfig.AccountUUID, driverConfig.AccountEmail, driverConfig.OrganizationUUID))
		}
		if driverConfig.RefreshToken != "" {
			refresher := BuildClaudeTokenRefresher(ctx, userID)
			driverOpts = append(driverOpts, llm.WithTokenRefresher(refresher, driverConfig.RefreshToken, driverConfig.TokenExpiresAt))
		}

		// Add temperature if specified in preference AND supported by the model.
		// Some models only support their default temperature and will hard-error if we send a value.
		if pref.Temperature != nil {
			switch model.TemperatureMode {
			case "", models.TemperatureModeAny:
				driverOpts = append(driverOpts, llm.WithTemperature(*pref.Temperature))
			case models.TemperatureModeOmit:
				// Intentionally omit temperature
			default:
				// Unknown mode: fail safe by omitting temperature
			}
		}

		// Add disable cache if specified
		if pref.DisableCache {
			driverOpts = append(driverOpts, llm.WithDisableCache())
		}

		// Handle thinking mode configuration
		driverOpts = append(driverOpts, llm.WithReasoningEffort(pref.ThinkingMode))

		// Merge with any additional options passed in
		driverOpts = append(driverOpts, opts...)

		agentProvider, err := GetDriverForModel(model, models.Family(driverConfig.DriverID), driverOpts...)
		logging.Debug("provider, err", "provider", agentProvider, "err", err)
		if err != nil {
			continue
		}

		// Success!
		return agentProvider, nil
	}

	return nil, fmt.Errorf("failed to get driver for preferences: no suitable models available: %v", preferences[0])
}
