// Copyright (c) 2025 Reliant Labs

// Package modelprefs reads the user's Settings → Model preferences
// (`model.tag_config.<tag>`) on the server, so every LLM call — a chat turn,
// a spawned sub-agent, a scheduled or API-originated run — honors them, not
// only the browser composer that happens to have written them.
//
// Precedence, highest first: call_llm node arg > model value > tag preference
// (this package) > tier/model default. This package only supplies the tag
// preference layer; callers own the layers above it.
//
//forge:exclude-contract: decodes and applies tag preference settings over the consumer-declared SettingsReader; pure precedence logic
package modelprefs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// KeyPrefix namespaces one settings row per tag, written by the web Settings
// → Model preferences panel (ModelPreferences.tsx `saveTagConfig`).
const KeyPrefix = "model.tag_config."

// SettingsReader is the one thing this package needs from the repository.
type SettingsReader interface {
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*db.Setting, error)
}

// TagPrefs is one tag's preference. Zero values mean "not set".
type TagPrefs struct {
	ModelID             string   `json:"model_id,omitempty"`
	ThinkingLevel       string   `json:"thinking_level,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	CompactionThreshold *int32   `json:"compaction_threshold,omitempty"`
	// Providers mirrors ModelSelector.Providers for the pinned model_id. A
	// local model is "<name>@local" with Providers ["local:<daemonID>"]: the
	// id alone does not say which machine serves it.
	Providers []string `json:"providers,omitempty"`
}

// IsZero reports whether the preference sets nothing.
func (p TagPrefs) IsZero() bool {
	return p.ModelID == "" && p.ThinkingLevel == "" && p.Temperature == nil && p.CompactionThreshold == nil && len(p.Providers) == 0
}

// Decode parses one settings row's value. Empty / "null" / "{}" decode to the
// zero preference rather than an error.
func Decode(value string) (TagPrefs, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" {
		return TagPrefs{}, nil
	}
	var prefs TagPrefs
	if err := json.Unmarshal([]byte(trimmed), &prefs); err != nil {
		return TagPrefs{}, fmt.Errorf("not a valid model preference object: %w", err)
	}
	return prefs, nil
}

// LoadAll reads every tag preference for a user in one query, keyed by tag.
// A malformed row is reported in the returned error (joined) but never stops
// the others from loading: the settings table is user-writable and outlives
// the code that wrote it, so one bad row degrades to "that tag keeps its
// defaults".
func LoadAll(ctx context.Context, reader SettingsReader, userID string) (map[string]TagPrefs, error) {
	if reader == nil || userID == "" {
		return nil, nil
	}
	rows, err := reader.ListSettingsByKey(ctx, userID, KeyPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("loading model tag preferences: %w", err)
	}
	loaded := make(map[string]TagPrefs, len(rows))
	var problems []error
	for _, row := range rows {
		if row == nil {
			continue
		}
		tag, ok := strings.CutPrefix(row.Key, KeyPrefix)
		if !ok || tag == "" {
			continue
		}
		prefs, err := Decode(row.Value)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", row.Key, err))
			continue
		}
		if prefs.IsZero() {
			continue
		}
		loaded[tag] = prefs
	}
	return loaded, errors.Join(problems...)
}

// TagFor returns the tag whose preference applies to a selector: the FIRST
// tag. A multi-tag selector is a scoring blend, not an ordered ladder of
// separate tiers, so only its leading tag names "the tier" the user tuned.
// Id-based selectors have no tier and return "".
func TagFor(selector models.ModelSelector) string {
	if selector.ID != "" || len(selector.Tags) == 0 {
		return ""
	}
	return selector.Tags[0]
}

// IsLocalModel reports whether model_id names a model served by one of the
// user's daemons ("<name>@local" or a "local:<daemonID>" provider). Such a
// model never resolves through the registry; see LocalSelector.
func (p TagPrefs) IsLocalModel() bool {
	return p.ModelID != "" && local.IsSelector(p.LocalSelector())
}

// LocalSelector is the selector naming the pinned model, providers included.
func (p TagPrefs) LocalSelector() models.ModelSelector {
	return models.ModelSelector{ID: p.ModelID, Providers: p.Providers}
}

// PreferredModel resolves the preference's model_id against the user's
// available providers. It returns nil when no model_id is set or it does not
// resolve (model gone from the registry, or no configured provider serves it),
// in which case the caller falls back to normal tag resolution. The returned
// resolution is by-id, so its ThinkingLevel is the model's capability default,
// not the tier entry's.
func (p TagPrefs) PreferredModel(registry *models.ModelRegistry, selector models.ModelSelector, providers []string) *models.ResolvedModel {
	if p.ModelID == "" || p.IsLocalModel() {
		return nil
	}
	providerPins := selector.Providers
	if len(p.Providers) > 0 {
		providerPins = p.Providers
	}
	resolved, err := registry.Resolve(models.ModelSelector{
		ID:                    p.ModelID,
		Providers:             providerPins,
		RequireOutputModality: selector.RequireOutputModality,
	}, providers)
	if err != nil {
		return nil
	}
	return resolved
}

// ValidThinkingLevel returns the preference's thinking level, or "" when it is
// unset or not a level at all.
func (p TagPrefs) ValidThinkingLevel() string {
	if models.IsKnownThinkingLevel(p.ThinkingLevel) {
		return p.ThinkingLevel
	}
	return ""
}
