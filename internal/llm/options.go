// Copyright (c) 2025 Reliant Labs
package llm

import (
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

// WithDisableCache disables caching for Anthropic
func WithDisableCache() DriverOption {
	return func(c *DriverOptions) {
		c.DisableCache = true
	}
}

func WithReasoningEffort(effort string) DriverOption {
	return func(options *DriverOptions) {
		defaultReasoningEffort := "medium"
		switch effort {
		case "low", "medium", "high", "xhigh":
			defaultReasoningEffort = effort
		default:
			logging.Warn("Invalid reasoning effort, using default: medium")
		}
		options.ReasoningEffort = defaultReasoningEffort
	}
}

// WithReasoningDisabled explicitly disables reasoning/thinking for the driver.
// This prevents any thinking budget from being allocated.
func WithReasoningDisabled() DriverOption {
	return func(options *DriverOptions) {
		options.ReasoningEffort = "disabled"
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

// WithTokenRefresher sets the OAuth token refresh callback and current token state.
// The refresher is called when the access token is expired, and should persist new tokens to DB.
func WithTokenRefresher(refresher func(refreshToken string) (string, time.Time, error), refreshToken string, expiresAt time.Time) DriverOption {
	return func(opts *DriverOptions) {
		opts.TokenRefresher = refresher
		opts.RefreshToken = refreshToken
		opts.TokenExpiresAt = expiresAt
	}
}
