// Copyright (c) 2025 Reliant Labs
package features

import (
	"context"

	"github.com/reliant-labs/reliant/internal/features/types"
)

// FlagType represents the type of a feature flag value
type FlagType string

const (
	FlagTypeBool   FlagType = "bool"
	FlagTypeString FlagType = "string"
	FlagTypeInt    FlagType = "int"
	FlagTypeFloat  FlagType = "float"
	FlagTypeJSON   FlagType = "json"
)

// Flag represents a feature flag configuration
type Flag struct {
	Key          string                 `json:"key" yaml:"key"`
	Name         string                 `json:"name" yaml:"name"`
	Description  string                 `json:"description" yaml:"description"`
	Type         FlagType               `json:"type" yaml:"type"`
	DefaultValue interface{}            `json:"default_value" yaml:"default_value"`
	Status       string                 `json:"status" yaml:"status"`
	Category     string                 `json:"category" yaml:"category"`
	Owner        string                 `json:"owner" yaml:"owner"`
	Tags         []string               `json:"tags" yaml:"tags"`
	Metadata     map[string]interface{} `json:"metadata" yaml:"metadata"`
}

// EvaluationContext is an alias for the shared type
type EvaluationContext = types.EvaluationContext

// Provider defines the interface for feature flag providers
type Provider interface {
	// Name returns the provider name
	Name() string

	// Priority returns the provider priority (higher = evaluated first)
	Priority() int

	// Initialize sets up the provider
	Initialize(ctx context.Context, config map[string]interface{}) error

	// EvaluateBool evaluates a boolean feature flag
	EvaluateBool(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue bool) (bool, error)

	// EvaluateString evaluates a string feature flag
	EvaluateString(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue string) (string, error)

	// EvaluateInt evaluates an integer feature flag
	EvaluateInt(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue int) (int, error)

	// EvaluateFloat evaluates a float feature flag
	EvaluateFloat(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue float64) (float64, error)

	// EvaluateJSON evaluates a JSON feature flag
	EvaluateJSON(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue interface{}) (interface{}, error)

	// HealthCheck checks if the provider is healthy
	HealthCheck(ctx context.Context) error

	// Shutdown cleanly shuts down the provider
	Shutdown(ctx context.Context) error
}

// Registry manages multiple feature flag providers
type Registry interface {
	// RegisterProvider registers a new provider
	RegisterProvider(provider Provider) error

	// UnregisterProvider removes a provider
	UnregisterProvider(name string) error

	// GetProvider gets a provider by name
	GetProvider(name string) Provider

	// ListProviders lists all registered providers
	ListProviders() []Provider

	// EvaluateBool evaluates a boolean flag across all providers
	EvaluateBool(ctx context.Context, key string, defaultValue bool) bool

	// EvaluateBoolWithContext evaluates a boolean flag with context
	EvaluateBoolWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue bool) bool

	// EvaluateString evaluates a string flag across all providers
	EvaluateString(ctx context.Context, key string, defaultValue string) string

	// EvaluateStringWithContext evaluates a string flag with context
	EvaluateStringWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue string) string

	// EvaluateInt evaluates an integer flag across all providers
	EvaluateInt(ctx context.Context, key string, defaultValue int) int

	// EvaluateIntWithContext evaluates an integer flag with context
	EvaluateIntWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue int) int

	// EvaluateFloat evaluates a float flag across all providers
	EvaluateFloat(ctx context.Context, key string, defaultValue float64) float64

	// EvaluateFloatWithContext evaluates a float flag with context
	EvaluateFloatWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue float64) float64

	// EvaluateJSON evaluates a JSON flag across all providers
	EvaluateJSON(ctx context.Context, key string, defaultValue interface{}) interface{}

	// EvaluateJSONWithContext evaluates a JSON flag with context
	EvaluateJSONWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue interface{}) interface{}

	// Shutdown shuts down all providers
	Shutdown(ctx context.Context) error
}

// NewEvaluationContext creates a new evaluation context (alias to types.NewEvaluationContext)
var NewEvaluationContext = types.NewEvaluationContext
