// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/features"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// BuildAvailableDrivers creates an AvailableDrivers struct from configured API keys
// and local provider configuration.
// userID is required to fetch API keys from the database.
func BuildAvailableDrivers(ctx context.Context, repo db.Repository, userID string) (models.AvailableDrivers, error) {
	if userID == "" {
		// Per reliant.md: no fallback paths, error out when things aren't set properly
		return models.AvailableDrivers{}, fmt.Errorf("userID is required to fetch API keys from database")
	}

	// Get the list of configured providers (with masked keys to know which ones are configured)
	maskedKeys, err := repo.GetProviderAPIKeys(ctx, userID)
	if err != nil {
		return models.AvailableDrivers{}, fmt.Errorf("failed to get provider API keys: %w", err)
	}

	drivers := make(map[models.DriverID]models.DriverConfig)

	// For each configured provider, get the credential material needed by the driver.
	for driverID := range maskedKeys {
		if driverID == "claude" {
			tokens, err := repo.GetClaudeAuthTokens(ctx, userID)
			if err != nil || tokens == nil {
				continue
			}
			accessToken := strings.TrimSpace(tokens.AccessToken)
			if accessToken == "" {
				continue
			}
			// Don't skip expired tokens here — the transport interceptor will refresh them.
			// Only skip if there's no refresh token AND the token is expired.
			if claude.IsTokenExpired(tokens.ExpiresAt) && strings.TrimSpace(tokens.RefreshToken) == "" {
				continue
			}
			// Claude OAuth tokens are sk-ant-oat keys that route to the anthropic driver
			// (which auto-detects and uses ClaudeCodeClient for sk-ant-oat keys).
			// We register under the "anthropic" driver ID so the resolver picks it up.
			drivers[models.DriverID("anthropic")] = models.DriverConfig{
				DriverID:         models.DriverID("anthropic"),
				APIKey:           accessToken,
				Enabled:          true,
				AccountUUID:      tokens.AccountUUID,
				AccountEmail:     tokens.AccountEmail,
				OrganizationUUID: tokens.OrganizationUUID,
				RefreshToken:     tokens.RefreshToken,
				TokenExpiresAt:   tokens.ExpiresAt,
			}
			continue
		}

		if driverID == "codex" {
			tokens, err := repo.GetCodexAuthTokens(ctx, userID)
			if err != nil || tokens == nil {
				continue
			}
			accessToken := strings.TrimSpace(tokens.AccessToken)
			if accessToken == "" || codex.IsTokenExpired(accessToken) {
				continue
			}
			drivers[models.DriverID(driverID)] = models.DriverConfig{
				DriverID: models.DriverID(driverID),
				APIKey:   accessToken,
				Enabled:  true,
			}
			continue
		}

		if driverID == "reliant" {
			config, ok := buildReliantDriverConfig(ctx, repo, userID)
			if ok {
				drivers[config.DriverID] = config
			}
			continue
		}

		// Get the actual unmasked API key for this provider
		apiKey, err := repo.GetProviderAPIKey(ctx, userID, driverID)
		if err != nil {
			continue // Skip this provider if we can't get the key
		}

		if apiKey != "" && apiKey != "dummy" { // Skip empty or dummy keys
			config := models.DriverConfig{
				DriverID: models.DriverID(driverID),
				APIKey:   apiKey,
				Enabled:  true,
			}

			// Add special base URLs for certain drivers
			switch driverID {
			case "openrouter":
				config.BaseURL = "https://openrouter.ai/api/v1"
				// case "groq":
				// 	config.BaseURL = "https://api.groq.com/openai/v1"
				// case "xai":
				// 	config.BaseURL = "https://api.x.ai/v1"
			}

			drivers[models.DriverID(driverID)] = config
		}
	}

	// Add local driver if configured
	// Local drivers use BaseURL instead of API key
	localConfig := local.GetLocalConfig()
	if localConfig != nil && localConfig.BaseURL != "" {
		drivers[models.DriverID("local")] = models.DriverConfig{
			DriverID: models.DriverID("local"),
			BaseURL:  localConfig.BaseURL,
			Enabled:  true,
			// No API key required for local drivers
		}
	}

	if len(drivers) == 0 {
		return models.AvailableDrivers{Drivers: make(map[models.DriverID]models.DriverConfig)}, nil
	}
	return models.AvailableDrivers{Drivers: drivers}, nil
}

func buildReliantDriverConfig(ctx context.Context, repo db.Repository, userID string) (models.DriverConfig, bool) {
	if !features.IsReliantManagedAccessEnabledForContext(ctx, repo, userID) {
		return models.DriverConfig{}, false
	}
	controlPlaneClient, runtimeBaseURL := getReliantRuntimeDependencies()
	if controlPlaneClient == nil || !controlPlaneClient.Enabled() {
		return models.DriverConfig{}, false
	}
	runtimeBaseURL = strings.TrimRight(strings.TrimSpace(runtimeBaseURL), "/")
	if runtimeBaseURL == "" {
		return models.DriverConfig{}, false
	}

	storedToken, err := repo.GetProviderAPIKey(ctx, userID, "reliant")
	if err != nil {
		logging.Warn("Failed to load stored Reliant token for runtime exchange", "user_id", userID, "error", err)
		return models.DriverConfig{}, false
	}
	storedToken = strings.TrimSpace(storedToken)
	if storedToken == "" || storedToken == "dummy" {
		return models.DriverConfig{}, false
	}

	access, err := controlPlaneClient.GetCurrentLLMAccess(ctx, storedToken)
	if err != nil {
		logReliantExchangeFailure(userID, err)
		return models.DriverConfig{}, false
	}
	apiKey := strings.TrimSpace(access.Key)
	if apiKey == "" {
		logging.Warn("Reliant runtime exchange returned empty key", "user_id", userID)
		return models.DriverConfig{}, false
	}

	allowedModels := make(map[models.ModelID]struct{}, len(access.AllowedModels))
	for _, modelID := range access.AllowedModels {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		allowedModels[models.ModelID(modelID)] = struct{}{}
	}

	return models.DriverConfig{
		DriverID:      models.DriverID("reliant"),
		APIKey:        apiKey,
		BaseURL:       runtimeBaseURL,
		Enabled:       true,
		AllowedModels: allowedModels,
	}, true
}

func logReliantExchangeFailure(userID string, err error) {
	level := "warn"
	if errorsIsNotConfigured(err) {
		level = "debug"
	}
	fields := []any{"user_id", userID, "error", err}
	if rpcErr, ok := err.(*controlplane.RPCError); ok {
		fields = append(fields, "status_code", rpcErr.StatusCode, "code", rpcErr.Code)
	}
	if level == "debug" {
		logging.Debug("Reliant runtime exchange unavailable", fields...)
		return
	}
	logging.Warn("Reliant runtime exchange failed", fields...)
}

func errorsIsNotConfigured(err error) bool {
	return err == controlplane.ErrNotConfigured
}

// BuildClaudeTokenRefresher returns a closure that refreshes Claude OAuth tokens
// and persists them to the database. The closure captures the global API key provider's
// repo and the userID so the transport interceptor can call it without DB knowledge.
func BuildClaudeTokenRefresher(ctx context.Context, userID string) func(refreshToken string) (string, time.Time, error) {
	return func(refreshToken string) (string, time.Time, error) {
		providerMu.Lock()
		repo := globalAPIKeyProvider.repo
		providerMu.Unlock()

		tokens, err := claude.RefreshClaudeTokens(refreshToken)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("claude token refresh failed: %w", err)
		}

		// Persist the refreshed tokens to the database
		dbTokens := db.ClaudeAuthTokens{
			AccessToken:      tokens.AccessToken,
			RefreshToken:     tokens.RefreshToken,
			ExpiresAt:        tokens.ExpiresAt,
			AccountUUID:      tokens.AccountUUID,
			AccountEmail:     tokens.AccountEmail,
			OrganizationUUID: tokens.OrganizationUUID,
			OrganizationName: tokens.OrganizationName,
			Scope:            tokens.Scope,
		}
		if err := repo.SetClaudeAuthTokens(ctx, userID, dbTokens); err != nil {
			logging.Warn("Failed to persist refreshed Claude tokens", "error", err)
			// Still return the new token even if DB persistence fails
		}

		logging.Info("Claude OAuth tokens refreshed and persisted", "user_id", userID, "expires_at", tokens.ExpiresAt)
		return tokens.AccessToken, tokens.ExpiresAt, nil
	}
}