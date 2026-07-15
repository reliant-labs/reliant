// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
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
				UserID:           userID,
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
				UserID:   userID,
				// The ChatGPT account id is the per-credential discriminator: it
				// makes each upstream account derive a distinct installation_id so
				// one Reliant user's multiple accounts don't share a device
				// fingerprint (Codex enforces one account per device).
				AccountUUID: tokens.AccountID,
			}
			continue
		}

		if driverID == "copilot" {
			// The Copilot credential is a GitHub OAuth token (from the device
			// flow in copilot/auth.go). The driver exchanges it for the
			// tier-appropriate bearer at request time.
			//
			// Prefer the dedicated Copilot credential store, which holds the GitHub
			// OAuth token. Auth is the raw gho_ Bearer against a single host — there
			// is no tier or session-token concept.
			if tokens, err := repo.GetCopilotAuthTokens(ctx, userID); err == nil && tokens != nil {
				githubToken := strings.TrimSpace(tokens.GitHubAccessToken)
				if githubToken != "" && githubToken != "dummy" {
					// GitHub token goes into APIKey, which the resolver forwards
					// via WithAPIKey; the driver reads it through
					// resolveGitHubToken (BearerToken/ApiKey).
					drivers[models.DriverID(driverID)] = models.DriverConfig{
						DriverID: models.DriverID(driverID),
						APIKey:   githubToken,
						Enabled:  true,
					}
					continue
				}
			}

			// Fall back to the generic provider-key / env path (dev / env-token
			// usage) when there is no copilot_auth_tokens row. The tier is left
			// empty here, so the driver infers it from the requested model.
			githubToken, err := repo.GetProviderAPIKey(ctx, userID, driverID)
			if err != nil || strings.TrimSpace(githubToken) == "" || githubToken == "dummy" {
				continue
			}
			drivers[models.DriverID(driverID)] = models.DriverConfig{
				DriverID: models.DriverID(driverID),
				APIKey:   githubToken,
				Enabled:  true,
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
			case "reliant":
				// Reliant routes to the admin-server proxy via RELIANT_API_BASE_URL.
				config.BaseURL = ResolveReliantBaseURL(apiKey)
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
