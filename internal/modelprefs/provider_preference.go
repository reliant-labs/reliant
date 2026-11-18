// Copyright (c) 2025 Reliant Labs
package modelprefs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ProviderPreferenceKey is the user's Settings → Model → "Default provider"
// row, written by the web Default models panel (ModelPreferences.tsx). Its
// value is {"providers":["<driver id>", …]}, most preferred first.
//
// It answers "which of my connected providers do auto tiers use?". Without it
// a tier resolves to the first entry of its models.yaml list that any
// connected provider serves, so a user with Claude and ChatGPT connected
// always got Claude in every tier Claude leads, with no way to choose.
const ProviderPreferenceKey = "model.provider_preference"

// ProviderPreference is the user's default provider order. Empty means
// "Auto": the tier's own order decides.
type ProviderPreference struct {
	Providers []string `json:"providers,omitempty"`
}

// IsZero reports whether the preference sets nothing.
func (p ProviderPreference) IsZero() bool { return len(p.Providers) == 0 }

// DecodeProviderPreference parses the setting's value. Empty / "null" / "{}"
// decode to Auto. Blank and duplicate entries are dropped, and so is any local
// provider: a local model is pinned per tier (TagPrefs.Providers names the
// machine), never reached through a tier's list.
func DecodeProviderPreference(value string) (ProviderPreference, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" {
		return ProviderPreference{}, nil
	}
	var raw ProviderPreference
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return ProviderPreference{}, fmt.Errorf("not a valid provider preference object: %w", err)
	}
	var out ProviderPreference
	for _, p := range raw.Providers {
		p = strings.TrimSpace(p)
		if p == "" || p == string(local.Family) || strings.HasPrefix(p, string(local.Family)+":") || slices.Contains(out.Providers, p) {
			continue
		}
		out.Providers = append(out.Providers, p)
	}
	return out, nil
}

// LoadProviderPreference reads the user's default provider order. A missing
// row is Auto; an unreadable one is Auto with the error returned, so callers
// log it and resolve as if it were unset.
//
// The key is matched exactly: ListSettingsByKey is a LIKE, and `_` is a LIKE
// wildcard. A user-level row (no project) wins over a project-scoped one,
// since the panel writes user-level and nothing scopes this per project.
func LoadProviderPreference(ctx context.Context, reader SettingsReader, userID string) (ProviderPreference, error) {
	if reader == nil || userID == "" {
		return ProviderPreference{}, nil
	}
	rows, err := reader.ListSettingsByKey(ctx, userID, ProviderPreferenceKey)
	if err != nil {
		return ProviderPreference{}, fmt.Errorf("loading default provider preference: %w", err)
	}
	value, found := "", false
	for _, row := range rows {
		if row == nil || row.Key != ProviderPreferenceKey {
			continue
		}
		if !found || row.ProjectID == nil {
			value, found = row.Value, true
		}
	}
	if !found {
		return ProviderPreference{}, nil
	}
	pref, err := DecodeProviderPreference(value)
	if err != nil {
		return ProviderPreference{}, fmt.Errorf("%s: %w", ProviderPreferenceKey, err)
	}
	return pref, nil
}

// Apply ranks the selector's providers by the preference when the selector
// does not already name its own: the registry then resolves a tier to the
// best model the preferred provider serves, and a named model to that
// provider when it serves it (models.ModelRegistry.Resolve). It is a
// preference, never a restriction: a tier or model the preferred providers
// cannot serve still resolves on any connected provider.
//
// A selector naming providers (`providers:`, or "<model>@<driver>") is a more
// specific choice and is returned unchanged, as is every selector when the
// preference is Auto. Callers apply the run's own provider (a chat on
// "gpt-…@codex") BEFORE this, so it, too, outranks the default.
func (p ProviderPreference) Apply(selector models.ModelSelector) models.ModelSelector {
	if p.IsZero() || len(selector.Providers) > 0 || strings.Contains(selector.ID, "@") {
		return selector
	}
	selector.Providers = slices.Clone(p.Providers)
	return selector
}
