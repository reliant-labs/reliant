// Copyright (c) 2025 Reliant Labs
package llm

import (
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Option is a function that configures DriverOptions
type DriverOption func(*DriverOptions)

// WithAPIKey sets the API key
func WithAPIKey(key string) DriverOption {
	return func(o *DriverOptions) {
		o.ApiKey = key
	}
}

// WithModel sets the model
func WithModel(model models.Model) DriverOption {
	return func(o *DriverOptions) {
		o.Model = model
	}
}

// WithMaxTokens sets the maximum tokens
func WithMaxTokens(maxTokens int64) DriverOption {
	return func(o *DriverOptions) {
		o.MaxTokens = maxTokens
	}
}

// WithForceToolChoice pins the request to a single named tool. See
// DriverOptions.ForceToolChoice for the one-shot-only constraint.
func WithForceToolChoice(name string) DriverOption {
	return func(o *DriverOptions) {
		o.ForceToolChoice = name
	}
}

// WithDisableCache disables caching for Anthropic
func WithDisableCache() DriverOption {
	return func(c *DriverOptions) {
		c.DisableCache = true
	}
}

// WithReasoningEffort sets the reasoning/thinking effort level.
//
// Accepted levels are models.KnownThinkingLevels ("low" through "ultra");
// per-model support is enforced upstream by the model's declared
// thinking_levels, not here. Empty (the UI's "Auto" choice — preferences that
// don't pin a level store "") and "auto" mean "no explicit preference" and
// auto-select the default (medium) silently. "off"/"none"/"disabled" normalize
// to "disabled", which drivers recognize as thinking-off. Only genuinely
// invalid values warn (with the offending value) before falling back to medium.
func WithReasoningEffort(effort string) DriverOption {
	return func(options *DriverOptions) {
		normalized := strings.ToLower(strings.TrimSpace(effort))
		if models.IsKnownThinkingLevel(normalized) {
			options.ReasoningEffort = normalized
			return
		}
		switch normalized {
		case "", "auto":
			// Auto/unset: default without warning.
			options.ReasoningEffort = "medium"
		case "off", "none", "disabled":
			options.ReasoningEffort = "disabled"
		default:
			logging.Warn("Invalid reasoning effort, using default",
				"effort", effort,
				"default", "medium")
			options.ReasoningEffort = "medium"
		}
	}
}

func WithExtraHeaders(headers map[string]string) DriverOption {
	return func(c *DriverOptions) {
		c.ExtraHeaders = headers
	}
}

func WithBearerToken(bearerToken string) DriverOption {
	return func(c *DriverOptions) {
		c.BearerToken = bearerToken
	}
}

// WithBaseURL sets a custom base URL for the OpenAI API
func WithBaseURL(url string) DriverOption {
	return func(c *DriverOptions) {
		c.BaseURL = url
	}
}

// WithTemperature sets the temperature for the driver
func WithTemperature(temp float64) DriverOption {
	return func(opts *DriverOptions) {
		opts.Temperature = &temp
	}
}

// WithSessionID sets the session ID for the driver
func WithSessionID(sessionID string) DriverOption {
	return func(opts *DriverOptions) {
		opts.SessionID = &sessionID
	}
}

// WithWorkingDirectory sets the working directory (project or worktree path)
func WithWorkingDirectory(path string) DriverOption {
	return func(opts *DriverOptions) {
		opts.WorkingDirectory = path
	}
}

// WithAccountMetadata sets Claude OAuth account metadata (replaces .claude.json)
func WithAccountMetadata(userID, accountUUID, accountEmail, organizationUUID string) DriverOption {
	return func(opts *DriverOptions) {
		opts.UserID = userID
		opts.AccountUUID = accountUUID
		opts.AccountEmail = accountEmail
		opts.OrganizationUUID = organizationUUID
	}
}

// WithTokenRefresher sets the OAuth token refresh/reload callbacks and current
// token state. The refresher is called when the access token is (nearly)
// expired; it coordinates the refresh (single-flight per user, cross-process
// via the persisted store) and returns the token state to use. The reloader
// re-reads the persisted tokens so drivers can recover from a 401 caused by
// another process rotating the tokens.
func WithTokenRefresher(refresher func(held OAuthTokens) (OAuthTokens, error), reloader func() (*OAuthTokens, error), refreshToken string, expiresAt time.Time) DriverOption {
	return func(opts *DriverOptions) {
		opts.TokenRefresher = refresher
		opts.TokenReloader = reloader
		opts.RefreshToken = refreshToken
		opts.TokenExpiresAt = expiresAt
	}
}
