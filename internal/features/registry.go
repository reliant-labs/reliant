// Copyright (c) 2025 Reliant Labs
package features

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

var (
	globalRegistry Registry
	once           sync.Once
)

// registry is the default implementation of Registry
type registry struct {
	providers map[string]Provider
	mu        sync.RWMutex
}

// NewRegistry creates a new feature flag registry
func NewRegistry() Registry {
	return &registry{
		providers: make(map[string]Provider),
	}
}

// GetGlobalRegistry returns the global feature flag registry
func GetGlobalRegistry() Registry {
	once.Do(func() {
		globalRegistry = NewRegistry()
	})
	return globalRegistry
}

func (r *registry) RegisterProvider(provider Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := provider.Name()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %s already registered", name)
	}

	r.providers[name] = provider
	return nil
}

func (r *registry) UnregisterProvider(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[name]; !exists {
		return fmt.Errorf("provider %s not found", name)
	}

	delete(r.providers, name)
	return nil
}

func (r *registry) GetProvider(name string) Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[name]
}

func (r *registry) ListProviders() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		providers = append(providers, p)
	}

	// Sort by priority (highest first)
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].Priority() > providers[j].Priority()
	})

	return providers
}

func (r *registry) EvaluateBool(ctx context.Context, key string, defaultValue bool) bool {
	// Use global user context if available
	globalCtx := GetGlobalUserContext()
	return r.EvaluateBoolWithContext(ctx, key, globalCtx, defaultValue)
}

func (r *registry) EvaluateBoolWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue bool) bool {
	providers := r.ListProviders()

	for _, provider := range providers {
		value, err := provider.EvaluateBool(ctx, key, evalCtx, defaultValue)
		if err == nil {
			return value
		}
		// Continue to next provider if this one fails
	}

	return defaultValue
}

func (r *registry) EvaluateString(ctx context.Context, key string, defaultValue string) string {
	// Use global user context if available
	globalCtx := GetGlobalUserContext()
	return r.EvaluateStringWithContext(ctx, key, globalCtx, defaultValue)
}

func (r *registry) EvaluateStringWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue string) string {
	providers := r.ListProviders()

	for _, provider := range providers {
		value, err := provider.EvaluateString(ctx, key, evalCtx, defaultValue)
		if err == nil {
			return value
		}
	}

	return defaultValue
}

func (r *registry) EvaluateInt(ctx context.Context, key string, defaultValue int) int {
	// Use global user context if available
	globalCtx := GetGlobalUserContext()
	return r.EvaluateIntWithContext(ctx, key, globalCtx, defaultValue)
}

func (r *registry) EvaluateIntWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue int) int {
	providers := r.ListProviders()

	for _, provider := range providers {
		value, err := provider.EvaluateInt(ctx, key, evalCtx, defaultValue)
		if err == nil {
			return value
		}
	}

	return defaultValue
}

func (r *registry) EvaluateFloat(ctx context.Context, key string, defaultValue float64) float64 {
	return r.EvaluateFloatWithContext(ctx, key, nil, defaultValue)
}

func (r *registry) EvaluateFloatWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue float64) float64 {
	providers := r.ListProviders()

	for _, provider := range providers {
		value, err := provider.EvaluateFloat(ctx, key, evalCtx, defaultValue)
		if err == nil {
			return value
		}
	}

	return defaultValue
}

func (r *registry) EvaluateJSON(ctx context.Context, key string, defaultValue interface{}) interface{} {
	// Use global user context if available
	globalCtx := GetGlobalUserContext()
	return r.EvaluateJSONWithContext(ctx, key, globalCtx, defaultValue)
}

func (r *registry) EvaluateJSONWithContext(ctx context.Context, key string, evalCtx *EvaluationContext, defaultValue interface{}) interface{} {
	providers := r.ListProviders()

	for _, provider := range providers {
		value, err := provider.EvaluateJSON(ctx, key, evalCtx, defaultValue)
		if err == nil {
			return value
		}
	}

	return defaultValue
}

func (r *registry) Shutdown(ctx context.Context) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var errs []error
	for name, provider := range r.providers {
		if err := provider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown provider %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errs)
	}
	return nil
}

// Helper functions for easy access to the global registry

// EvaluateBool evaluates a boolean feature flag using the global registry
func EvaluateBool(key string, defaultValue bool) bool {
	return GetGlobalRegistry().EvaluateBool(context.Background(), key, defaultValue)
}

// EvaluateString evaluates a string feature flag using the global registry
func EvaluateString(key string, defaultValue string) string {
	return GetGlobalRegistry().EvaluateString(context.Background(), key, defaultValue)
}

// EvaluateInt evaluates an integer feature flag using the global registry
func EvaluateInt(key string, defaultValue int) int {
	return GetGlobalRegistry().EvaluateInt(context.Background(), key, defaultValue)
}
