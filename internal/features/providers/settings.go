// Copyright (c) 2025 Reliant Labs
package providers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/features/types"
)

// SettingsProvider provides feature flags from the settings service
type SettingsProvider struct {
	settings types.SettingsGetter
	priority int
	prefix   string
}

// NewSettingsProvider creates a new settings-based provider
func NewSettingsProvider(priority int, settings types.SettingsGetter) *SettingsProvider {
	return &SettingsProvider{
		settings: settings,
		priority: priority,
		prefix:   "features.",
	}
}

func (p *SettingsProvider) Name() string {
	return "settings"
}

func (p *SettingsProvider) Priority() int {
	return p.priority
}

func (p *SettingsProvider) Initialize(ctx context.Context, config map[string]interface{}) error {
	if p.settings == nil {
		return fmt.Errorf("settings service is required")
	}

	// Optionally override prefix from config
	if config != nil {
		if prefix, ok := config["prefix"].(string); ok {
			p.prefix = prefix
		}
	}

	return nil
}

func (p *SettingsProvider) EvaluateBool(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue bool) (bool, error) {
	settingsKey := p.prefix + key

	value, err := p.settings.GetBool(ctx, settingsKey)
	if err != nil {
		// Not found or error - return default
		return defaultValue, fmt.Errorf("setting %s not found: %w", settingsKey, err)
	}

	return value, nil
}

func (p *SettingsProvider) EvaluateString(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue string) (string, error) {
	settingsKey := p.prefix + key

	value, err := p.settings.GetString(ctx, settingsKey)
	if err != nil || value == "" {
		return defaultValue, fmt.Errorf("setting %s not found: %w", settingsKey, err)
	}

	return value, nil
}

func (p *SettingsProvider) EvaluateInt(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue int) (int, error) {
	settingsKey := p.prefix + key

	value, err := p.settings.GetInt(ctx, settingsKey)
	if err != nil {
		return defaultValue, fmt.Errorf("setting %s not found: %w", settingsKey, err)
	}

	return value, nil
}

func (p *SettingsProvider) EvaluateFloat(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue float64) (float64, error) {
	settingsKey := p.prefix + key

	value, err := p.settings.GetFloat(ctx, settingsKey)
	if err != nil {
		return defaultValue, fmt.Errorf("setting %s not found: %w", settingsKey, err)
	}

	return value, nil
}

func (p *SettingsProvider) EvaluateJSON(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue interface{}) (interface{}, error) {
	// For JSON, we can store it as a string in settings
	settingsKey := p.prefix + key

	value, err := p.settings.GetString(ctx, settingsKey)
	if err != nil || value == "" {
		return defaultValue, fmt.Errorf("setting %s not found: %w", settingsKey, err)
	}

	// Return the string value - caller can parse as needed
	return value, nil
}

func (p *SettingsProvider) HealthCheck(ctx context.Context) error {
	// Check if settings service is accessible
	if p.settings == nil {
		return fmt.Errorf("settings service not available")
	}

	// Try to read a test key
	_, _ = p.settings.GetString(ctx, "health.check")
	return nil
}

func (p *SettingsProvider) Shutdown(ctx context.Context) error {
	// Nothing to clean up
	return nil
}
