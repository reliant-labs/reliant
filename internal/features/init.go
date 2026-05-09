// Copyright (c) 2025 Reliant Labs
package features

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/features/providers"
	"github.com/reliant-labs/reliant/internal/features/types"
	"github.com/reliant-labs/reliant/internal/logging"
)

var (
	globalUserContext *types.EvaluationContext
	userContextMu     sync.RWMutex
)

// InitializeFeatureFlags sets up the feature flag system with configured providers
func InitializeFeatureFlags(ctx context.Context) error {
	return InitializeFeatureFlagsWithSettings(ctx, nil)
}

// InitializeFeatureFlagsWithSettings sets up the feature flag system with settings service
func InitializeFeatureFlagsWithSettings(ctx context.Context, settings types.SettingsGetter) error {
	registry := GetGlobalRegistry()

	// Initialize static provider (lowest priority)
	staticProvider := providers.NewStaticProvider(100)
	staticConfig := map[string]interface{}{
		"flags": map[string]interface{}{
			// Default static flags
			"debug_enhanced":            false,
			"new_chat_ui":               false,
			"project_analyzer_disabled": true,  // TEMPORARY: Disable project analyzer and scanning
			"skills_enabled":            false, // Release-safe default: disable skills until explicitly enabled
		},
	}
	if err := staticProvider.Initialize(ctx, staticConfig); err != nil {
		logging.Warn("Failed to initialize static provider", "error", err)
	} else {
		_ = registry.RegisterProvider(staticProvider) //nolint:errcheck // Only fails on duplicate registration
	}

	// Try to get Statsig configuration from settings first
	var statsigKey, statsigEnv string

	if settings != nil {
		// Try to get from settings
		if key, err := settings.GetString(ctx, "experiments.statsig.client_key"); err == nil && key != "" {
			statsigKey = key
		}
		if env, err := settings.GetString(ctx, "experiments.statsig.environment"); err == nil && env != "" {
			statsigEnv = env
		}
	}

	// Fall back to env var if not in settings
	if statsigKey == "" {
		statsigKey = os.Getenv("STATSIG_CLIENT_KEY")
	}
	if statsigEnv == "" {
		statsigEnv = "production"
	}

	// Fall back to environment variables for development only
	if config.IsDevelopmentEnvironment() && os.Getenv("STATSIG_SERVER_SECRET_KEY") != "" {
		statsigKey = os.Getenv("STATSIG_SERVER_SECRET_KEY")
		if os.Getenv("STATSIG_ENVIRONMENT") != "" {
			statsigEnv = os.Getenv("STATSIG_ENVIRONMENT")
		}
	}

	// Initialize Statsig provider if configured (highest priority)
	// Statsig is disabled in non-production environments to avoid polluting experiments
	if statsigKey != "" && config.IsProductionEnvironment() {
		statsigProvider := providers.NewStatsigProvider(900)

		// Determine if it's a client key
		statsigConfig := map[string]interface{}{
			"environment": statsigEnv,
			"timeout":     "10s",
		}

		if strings.HasPrefix(statsigKey, "client-") {
			statsigConfig["client_key"] = statsigKey
		} else {
			statsigConfig["server_secret_key"] = statsigKey
		}
		if err := statsigProvider.Initialize(ctx, statsigConfig); err != nil {
			logging.Warn("[Statsig] Failed to initialize feature flag provider", "error", err)
		} else {
			_ = registry.RegisterProvider(statsigProvider) //nolint:errcheck // Only fails on duplicate registration
		}
	}

	// Settings-based provider (medium-high priority)
	if settings != nil {
		settingsProvider := providers.NewSettingsProvider(700, settings)
		if err := settingsProvider.Initialize(ctx, nil); err != nil {
			logging.Warn("Failed to initialize settings provider", "error", err)
		} else {
			_ = registry.RegisterProvider(settingsProvider) //nolint:errcheck // Only fails on duplicate registration
		}
	}

	// Environment variable provider (medium priority) - for development only
	if config.IsDevelopmentEnvironment() {
		envProvider := providers.NewEnvironmentProvider(600)
		if err := envProvider.Initialize(ctx, nil); err != nil {
			logging.Warn("Failed to initialize environment provider", "error", err)
		} else {
			_ = registry.RegisterProvider(envProvider) //nolint:errcheck // Only fails on duplicate registration
		}
	}

	return nil
}

// SetGlobalUserContext sets the global user context for feature flag evaluation
func SetGlobalUserContext(userID string) {
	userContextMu.Lock()
	defer userContextMu.Unlock()

	globalUserContext = types.NewEvaluationContext().WithUserID(userID)
}

// GetGlobalUserContext returns the current global user context
func GetGlobalUserContext() *types.EvaluationContext {
	userContextMu.RLock()
	defer userContextMu.RUnlock()

	if globalUserContext != nil {
		// Return a copy to avoid modifications
		copy := *globalUserContext
		return &copy
	}

	return nil
}

// NewEvaluationContextFromRequest creates an evaluation context from an HTTP request context
// It extracts the user ID from the auth context if available
func NewEvaluationContextFromRequest(ctx context.Context) *types.EvaluationContext {
	evalCtx := types.NewEvaluationContext()

	// Try to get user ID from auth context (v2 API)
	if userID, ok := ctx.Value("user_id").(string); ok && userID != "" {
		evalCtx.UserID = userID
	}

	return evalCtx
}

// Shutdown cleanly shuts down the feature flag system
func Shutdown(ctx context.Context) error {
	return GetGlobalRegistry().Shutdown(ctx)
}
