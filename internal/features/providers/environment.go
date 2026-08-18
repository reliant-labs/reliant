// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Registry/lookup-table package: the exported vars are populated once at init
// by the packages that register into them, then read. A getter returns the
// same map or slice header, so it moves the mutation surface without
// narrowing it.
package providers

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/features/types"
)

// EnvironmentProvider provides feature flags from environment variables
type EnvironmentProvider struct {
	prefix   string
	priority int
}

// NewEnvironmentProvider creates a new environment variable provider
func NewEnvironmentProvider(priority int) *EnvironmentProvider {
	return &EnvironmentProvider{
		prefix:   "RELIANT_FEATURE_",
		priority: priority,
	}
}

func (p *EnvironmentProvider) Name() string {
	return "environment"
}

func (p *EnvironmentProvider) Priority() int {
	return p.priority
}

func (p *EnvironmentProvider) Initialize(ctx context.Context, config map[string]interface{}) error {
	// Optionally override prefix from config
	if config != nil {
		if prefix, ok := config["prefix"].(string); ok {
			p.prefix = prefix
		}
	}
	return nil
}

func (p *EnvironmentProvider) EvaluateBool(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue bool) (bool, error) {
	envKey := p.formatEnvKey(key)
	value := os.Getenv(envKey)

	if value == "" {
		return defaultValue, fmt.Errorf("environment variable %s not set", envKey)
	}

	// Parse boolean values
	switch strings.ToLower(value) {
	case "true", "1", "yes", "on", "enabled":
		return true, nil
	case "false", "0", "no", "off", "disabled":
		return false, nil
	default:
		return defaultValue, fmt.Errorf("invalid boolean value: %s", value)
	}
}

func (p *EnvironmentProvider) EvaluateString(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue string) (string, error) {
	envKey := p.formatEnvKey(key)
	value := os.Getenv(envKey)

	if value == "" {
		return defaultValue, fmt.Errorf("environment variable %s not set", envKey)
	}

	return value, nil
}

func (p *EnvironmentProvider) EvaluateInt(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue int) (int, error) {
	envKey := p.formatEnvKey(key)
	value := os.Getenv(envKey)

	if value == "" {
		return defaultValue, fmt.Errorf("environment variable %s not set", envKey)
	}

	intVal, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue, fmt.Errorf("invalid integer value: %s", value)
	}

	return intVal, nil
}

func (p *EnvironmentProvider) EvaluateFloat(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue float64) (float64, error) {
	envKey := p.formatEnvKey(key)
	value := os.Getenv(envKey)

	if value == "" {
		return defaultValue, fmt.Errorf("environment variable %s not set", envKey)
	}

	floatVal, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return defaultValue, fmt.Errorf("invalid float value: %s", value)
	}

	return floatVal, nil
}

func (p *EnvironmentProvider) EvaluateJSON(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue interface{}) (interface{}, error) {
	// For JSON, we just return the string value and let the caller parse it
	envKey := p.formatEnvKey(key)
	value := os.Getenv(envKey)

	if value == "" {
		return defaultValue, fmt.Errorf("environment variable %s not set", envKey)
	}

	return value, nil
}

func (p *EnvironmentProvider) HealthCheck(ctx context.Context) error {
	// Environment variables are always available
	return nil
}

func (p *EnvironmentProvider) Shutdown(ctx context.Context) error {
	// Nothing to clean up
	return nil
}

// formatEnvKey converts a feature flag key to an environment variable name
func (p *EnvironmentProvider) formatEnvKey(key string) string {
	// Convert to uppercase and replace non-alphanumeric characters with underscores
	envKey := strings.ToUpper(key)
	envKey = strings.ReplaceAll(envKey, "-", "_")
	envKey = strings.ReplaceAll(envKey, ".", "_")
	return p.prefix + envKey
}
