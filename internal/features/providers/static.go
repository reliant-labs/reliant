// Copyright (c) 2025 Reliant Labs
package providers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/features/types"
)

// StaticProvider provides static feature flag values from configuration
type StaticProvider struct {
	flags    map[string]interface{}
	priority int
}

// NewStaticProvider creates a new static provider
func NewStaticProvider(priority int) *StaticProvider {
	return &StaticProvider{
		flags:    make(map[string]interface{}),
		priority: priority,
	}
}

func (p *StaticProvider) Name() string {
	return "static"
}

func (p *StaticProvider) Priority() int {
	return p.priority
}

func (p *StaticProvider) Initialize(ctx context.Context, config map[string]interface{}) error {
	// Load static flags from configuration
	if flags, ok := config["flags"].(map[string]interface{}); ok {
		p.flags = flags
	}
	return nil
}

func (p *StaticProvider) EvaluateBool(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue bool) (bool, error) {
	if val, ok := p.flags[key]; ok {
		if boolVal, ok := val.(bool); ok {
			return boolVal, nil
		}
	}
	return defaultValue, fmt.Errorf("flag %s not found", key)
}

func (p *StaticProvider) EvaluateString(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue string) (string, error) {
	if val, ok := p.flags[key]; ok {
		if strVal, ok := val.(string); ok {
			return strVal, nil
		}
	}
	return defaultValue, fmt.Errorf("flag %s not found", key)
}

func (p *StaticProvider) EvaluateInt(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue int) (int, error) {
	if val, ok := p.flags[key]; ok {
		switch v := val.(type) {
		case int:
			return v, nil
		case float64:
			return int(v), nil
		}
	}
	return defaultValue, fmt.Errorf("flag %s not found", key)
}

func (p *StaticProvider) EvaluateFloat(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue float64) (float64, error) {
	if val, ok := p.flags[key]; ok {
		switch v := val.(type) {
		case float64:
			return v, nil
		case int:
			return float64(v), nil
		}
	}
	return defaultValue, fmt.Errorf("flag %s not found", key)
}

func (p *StaticProvider) EvaluateJSON(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue interface{}) (interface{}, error) {
	if val, ok := p.flags[key]; ok {
		return val, nil
	}
	return defaultValue, fmt.Errorf("flag %s not found", key)
}

func (p *StaticProvider) HealthCheck(ctx context.Context) error {
	return nil
}

func (p *StaticProvider) Shutdown(ctx context.Context) error {
	return nil
}

// SetFlag sets a static flag value (useful for testing)
func (p *StaticProvider) SetFlag(key string, value interface{}) {
	p.flags[key] = value
}
