// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// BuildAvailableDrivers creates an AvailableDrivers struct from configured API keys
// and local provider configuration.
// userID is required to fetch API keys from the database.
func BuildAvailableDrivers(ctx context.Context, repo db.Repository, userID string) (models.AvailableDrivers, error) {
	return buildAvailableDrivers(ctx, repo, userID, AccountAvailabilityClient)
}

// AccountAvailabilityClient builds the credentialed client whose account
// catalog decides a dynamic provider's availability (BuildAvailableDrivers,
// ListAvailableModels). A variable, like GetDriver, so a test that resolves
// through GetAvailableDrivers never depends on a real provider's catalog
// answering — or refusing — its fake credential.
var AccountAvailabilityClient func(ctx context.Context, driverID models.DriverID, cfg models.DriverConfig) (registry.Client, error) = clientForDriver

// buildAvailableDrivers is BuildAvailableDrivers with the availability client
// builder injected, so tests never reach a provider's real catalog endpoint.
func buildAvailableDrivers(ctx context.Context, repo db.Repository, userID string, build clientBuilder) (models.AvailableDrivers, error) {
	drivers, err := configuredDrivers(ctx, repo, userID)
	if err != nil {
		return models.AvailableDrivers{}, err
	}
	return withAccountAvailability(ctx, drivers, build), nil
}

// usableAPIKey reports whether a stored api_keys value is a credential: not
// empty, not the "dummy" placeholder, and not an OAuth provider's connection
// marker (db.ProviderOAuthMarker).
func usableAPIKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && key != "dummy" && key != db.ProviderOAuthMarker
}

// configuredDrivers loads the credential material for every provider the user
// has stored a key or OAuth connection for.
func configuredDrivers(ctx context.Context, repo db.Repository, userID string) (map[models.DriverID]models.DriverConfig, error) {
	if userID == "" {
		// Per reliant.md: no fallback paths, error out when things aren't set properly
		return nil, fmt.Errorf("userID is required to fetch API keys from database")
	}

	// Get the list of configured providers (with masked keys to know which ones are configured)
	maskedKeys, err := repo.GetProviderAPIKeys(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider API keys: %w", err)
	}

	drivers := make(map[models.DriverID]models.DriverConfig)

	// For each configured provider, get the credential material needed by the driver.
	for driverID := range maskedKeys {
		// A provider is available only if its driver is registered. Credentials
		// for catalog-only providers (xai, groq, azure, bedrock) must not make
		// their models selectable: every call would fail at resolution.
		if !hasRegisteredDriver(driverID) {
			continue
		}

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
			if accessToken == "" {
				continue
			}
			// Don't skip expired tokens here — the transport interceptor will
			// refresh them. Only skip if there's no refresh token AND the token
			// is expired, which is a session that cannot be recovered without
			// the user reconnecting Codex.
			if codex.IsTokenExpired(accessToken) && strings.TrimSpace(tokens.RefreshToken) == "" {
				continue
			}
			// Codex access tokens are JWTs carrying their own exp claim, so the
			// expiry is derived rather than stored (codex_auth_tokens has no
			// expires_at column). An unparseable token yields the zero time,
			// which the refresh transport treats as "refresh before using".
			expiresAt, err := codex.GetTokenExpiry(accessToken)
			if err != nil {
				expiresAt = time.Time{}
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
				AccountUUID:    tokens.AccountID,
				RefreshToken:   tokens.RefreshToken,
				TokenExpiresAt: expiresAt,
			}
			continue
		}

		if driverID == antigravity.DriverID {
			tokens, err := repo.GetAntigravityAuthTokens(ctx, userID)
			if err != nil || tokens == nil {
				continue
			}
			accessToken := strings.TrimSpace(tokens.AccessToken)
			if accessToken == "" {
				continue
			}
			// Don't skip expired tokens here — the transport interceptor will
			// refresh them. Only skip when there is no refresh token AND the
			// token is expired, which is a session the user must reconnect.
			if antigravity.IsTokenExpired(tokens.ExpiresAt) && strings.TrimSpace(tokens.RefreshToken) == "" {
				continue
			}
			drivers[models.DriverID(driverID)] = models.DriverConfig{
				DriverID:       models.DriverID(driverID),
				APIKey:         accessToken,
				Enabled:        true,
				UserID:         userID,
				RefreshToken:   tokens.RefreshToken,
				TokenExpiresAt: tokens.ExpiresAt,
			}
			continue
		}

		if driverID == "copilot" {
			// The Copilot credential is the GitHub OAuth token from the device
			// flow (copilot/auth.go), held only in copilot_auth_tokens. Auth is
			// the raw gho_ Bearer against a single host. The api_keys row is
			// the connection marker, never a token: without a token row
			// Copilot is not connected, exactly as Settings reports it.
			tokens, err := repo.GetCopilotAuthTokens(ctx, userID)
			if err != nil || tokens == nil {
				continue
			}
			githubToken := strings.TrimSpace(tokens.GitHubAccessToken)
			if !usableAPIKey(githubToken) {
				continue
			}
			// GitHub token goes into APIKey, which the resolver forwards via
			// WithAPIKey; the driver reads it through resolveGitHubToken.
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

		if usableAPIKey(apiKey) {
			// The gateway authenticates only rlat_ access tokens (401 otherwise), so a
			// legacy-shaped key is not a usable credential. Never log the key.
			if driverID == "reliant" && !IsReliantLLMKey(apiKey) {
				healed, configured, healErr := HealReliantKey(ctx, userID)
				if healErr == nil && IsReliantLLMKey(healed) {
					apiKey = healed
				} else {
					logging.Warn("Skipping reliant provider key: not an rlat_ access token and it could not be healed",
						"user_id", userID, "reason", "legacy_reliant_key_format",
						"heal_configured", configured, "error", healErr)
					continue
				}
			}
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

	return drivers, nil
}

// withAccountAvailability asks each configured dynamic provider (Copilot,
// Codex: the clients implementing registry.AvailabilityReporter) what the
// account may use, and applies the answer. Each report is TTL-cached by its
// driver, so a warm resolution makes no network call.
//
// A failed report is either a VERDICT or an OUTAGE, and the two must not be
// confused:
//
//   - Verdict (registry.ErrCredentialRejected: the provider answered 400, 401
//     or 403 to the credential every request would carry). The provider is
//     unavailable: it leaves Drivers, so no resolution can route to it, and
//     Unavailable records why, for the error that names the fix. Treating it
//     as servable is what sent chat dfd85515's sub-agents to a Copilot that
//     answered every request "Authorization header is badly formatted".
//   - Outage (transport error, timeout, 429, 5xx, an unparseable body). The
//     provider stays, unfiltered: the credential is not in question, and a
//     catalog blip must never strand a chat on an otherwise healthy provider.
func withAccountAvailability(ctx context.Context, drivers map[models.DriverID]models.DriverConfig, build clientBuilder) models.AvailableDrivers {
	reports := make(map[string]registry.ProviderAvailability)
	var unavailable map[models.DriverID]string
	for driverID, cfg := range drivers {
		if !cfg.IsConfigured() {
			continue
		}
		client, err := build(ctx, driverID, cfg)
		if err != nil {
			continue
		}
		reporter, ok := client.(registry.AvailabilityReporter)
		if !ok {
			continue
		}
		report, err := reporter.ReportAvailability(ctx)
		var rejected *registry.CredentialRejectedError
		if errors.As(err, &rejected) {
			reason := fmt.Sprintf("%s rejected the saved credential (HTTP %d: %s)",
				models.ProviderDisplayName(string(driverID)), rejected.Status, rejected.Detail)
			logging.Warn("provider rejected the stored credential; provider unavailable until reconnected",
				"driver", string(driverID), "error", err)
			if unavailable == nil {
				unavailable = make(map[models.DriverID]string)
			}
			unavailable[driverID] = reason
			continue
		}
		if err != nil {
			logging.Warn("provider availability report failed (outage, not a credential verdict); treating its models as servable",
				"driver", string(driverID), "error", err)
			continue
		}
		reports[string(driverID)] = report
	}
	for driverID := range unavailable {
		delete(drivers, driverID)
	}

	available := models.AvailableDrivers{Drivers: drivers, Unavailable: unavailable}
	if reg, err := models.GetRegistry(); err == nil {
		available.Availability = registry.BuildAvailabilityFunc(reg, reports)
	}
	return available
}

// hasRegisteredDriver reports whether a stored provider id has a registered
// driver factory. The "claude" credential routes to the anthropic driver.
func hasRegisteredDriver(providerID string) bool {
	family := models.Family(providerID)
	if providerID == "claude" {
		family = models.Family("anthropic")
	}
	_, ok := registry.GetDriverFactory(family)
	return ok
}

// HasStaleReliantKey reports whether the user has a stored `reliant` provider
// key that the gateway would reject (not an rlat_ access token). It is the
// condition under which BuildAvailableDrivers silently drops the provider.
func HasStaleReliantKey(ctx context.Context, repo db.Repository, userID string) (bool, error) {
	masked, err := repo.GetProviderAPIKeys(ctx, userID)
	if err != nil {
		return false, err
	}
	if _, ok := masked["reliant"]; !ok {
		return false, nil
	}
	apiKey, err := repo.GetProviderAPIKey(ctx, userID, "reliant")
	if err != nil {
		return false, err
	}
	apiKey = strings.TrimSpace(apiKey)
	return apiKey != "" && apiKey != "dummy" && !IsReliantLLMKey(apiKey), nil
}

// NewLocalDriver builds a driver for a local model synthesized for this
// request. Local models are never in the registry or DriverMapping (they
// belong to a user's daemons), so GetDriverForModel's CanDriverUseModel gate
// does not apply; the caller supplies the relay via llm.WithTransport.
func NewLocalDriver(model models.Model, opts ...llm.DriverOption) (llm.Driver, error) {
	options := llm.DriverOptions{}
	for _, o := range opts {
		o(&options)
	}
	options.Model = model
	return &baseDriver{client: local.NewClient(options), model: model}, nil
}
