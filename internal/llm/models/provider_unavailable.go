// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// providerDisplayNames are the names a user sees in Settings → Providers, keyed
// by driver id. "anthropic" serves both a Claude Code sign-in and an Anthropic
// API key, so it names both.
var providerDisplayNames = map[string]string{
	"anthropic":   "Anthropic (Claude)",
	"antigravity": "Antigravity",
	"codex":       "Codex (ChatGPT)",
	"copilot":     "GitHub Copilot",
	"gemini":      "Google Gemini",
	"local":       "Local models",
	"openai":      "OpenAI",
	"openrouter":  "OpenRouter",
	"reliant":     "Reliant",
	"vertexai":    "Vertex AI",
}

// ProviderDisplayName returns the user-facing name of a driver, or the driver
// id itself when it has none.
func ProviderDisplayName(driver string) string {
	if name, ok := providerDisplayNames[driver]; ok {
		return name
	}
	return driver
}

// ProviderUnavailableError is a model that names its provider(s) — an explicit
// "model@driver" pin — when none of them can serve the account. It matches
// drivererrors.ErrNoServableProvider, so the workflow runtime fails the call
// at once rather than retrying a resolution that cannot change in seconds.
type ProviderUnavailableError struct {
	ModelID   string
	Providers []string
}

func (e *ProviderUnavailableError) Error() string {
	return fmt.Sprintf("none of required providers %v available for model %s", e.Providers, e.ModelID)
}

// Is makes errors.Is(err, drivererrors.ErrNoServableProvider) hold.
func (e *ProviderUnavailableError) Is(target error) bool {
	return target == drivererrors.ErrNoServableProvider
}

// Explain renders the failure as the user should read it: which provider the
// model is pinned to, why that provider cannot serve (reasons, keyed by
// driver, from AvailableDrivers.Unavailable; absent means "not connected"),
// and the two ways out.
func (e *ProviderUnavailableError) Explain(reasons map[DriverID]string) string {
	return fmt.Sprintf("%s. Reconnect %s in Settings → Providers, or pick a model from a connected provider in the composer's model picker",
		e.Reason(reasons), e.ProviderNames())
}

// Reason is the first clause of Explain: the provider(s) the model is pinned
// to and why none of them can serve it, without the ways out.
func (e *ProviderUnavailableError) Reason(reasons map[DriverID]string) string {
	whys := make([]string, 0, len(e.Providers))
	for _, p := range e.Providers {
		if reason, ok := reasons[DriverID(p)]; ok && reason != "" {
			whys = append(whys, reason)
		} else {
			whys = append(whys, ProviderDisplayName(p)+" is not connected")
		}
	}
	return fmt.Sprintf("%s runs only on %s, and %s", e.ModelID, e.ProviderNames(), strings.Join(whys, "; "))
}

// ProviderNames is the pinned provider(s) as Settings → Providers names them.
func (e *ProviderUnavailableError) ProviderNames() string {
	names := make([]string, 0, len(e.Providers))
	for _, p := range e.Providers {
		names = append(names, ProviderDisplayName(p))
	}
	return strings.Join(names, " or ")
}
