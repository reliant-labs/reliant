// Copyright (c) 2025 Reliant Labs
package models

import (
	"slices"
	"strings"
)

// ModelAvailability is what a credentialed provider reports about one model for
// the connected account: whether it may be used at all, why not, and the real
// context window the account gets.
type ModelAvailability struct {
	// Disabled means the account may not use the model on this provider.
	Disabled bool
	// Reason is the user-facing explanation for Disabled.
	Reason string
	// ContextWindow, when positive, is the window this account actually gets
	// from the provider; it replaces the catalog's per-provider window.
	ContextWindow int
	// ThinkingLevels, when non-empty, are the reasoning levels the account can
	// actually use, replacing the catalog's list.
	ThinkingLevels []string
}

// AvailabilityFunc reports per-(driver, model id) availability for the current
// account. It must be cheap: resolution calls it per candidate, so providers
// back it with a TTL cache rather than a request. A nil func, or a driver/model
// it has no opinion on, means "servable".
type AvailabilityFunc func(driver, modelID string) ModelAvailability

// restrictProviders returns the available-provider set with every provider the
// filter disables for this model removed, plus the first disabling reason.
func restrictProviders(model *ModelDefinition, availableSet map[string]bool, avail AvailabilityFunc) (map[string]bool, string) {
	if avail == nil {
		return availableSet, ""
	}
	var (
		restricted map[string]bool
		reason     string
	)
	for _, p := range model.Providers {
		if !availableSet[p.Driver] {
			continue
		}
		a := avail(p.Driver, model.ID)
		if !a.Disabled {
			continue
		}
		if restricted == nil {
			restricted = make(map[string]bool, len(availableSet))
			for k, v := range availableSet {
				restricted[k] = v
			}
		}
		restricted[p.Driver] = false
		if reason == "" {
			reason = a.Reason
		}
	}
	if restricted == nil {
		return availableSet, ""
	}
	return restricted, reason
}

// resolvedWithAvailability builds the ResolvedModel, applying the account's real
// context window to the chosen provider so EffectiveContextWindow (compaction
// threshold, trim backstop) is derived from what the account will actually get.
func resolvedWithAvailability(model *ModelDefinition, provider *ProviderMapping, level string, avail AvailabilityFunc) *ResolvedModel {
	resolved := &ResolvedModel{Definition: *model, Provider: *provider, ThinkingLevel: level}
	if avail == nil {
		return resolved
	}
	a := avail(provider.Driver, model.ID)
	if a.ContextWindow > 0 {
		resolved.Provider.MaxContextWindow = a.ContextWindow
		resolved.Definition.Providers = slices.Clone(model.Providers)
		for i := range resolved.Definition.Providers {
			if resolved.Definition.Providers[i].Driver == provider.Driver {
				resolved.Definition.Providers[i].MaxContextWindow = a.ContextWindow
			}
		}
	}
	if len(a.ThinkingLevels) > 0 {
		resolved.Definition.Capabilities.ThinkingLevels = slices.Clone(a.ThinkingLevels)
		resolved.ThinkingLevel = ClampThinkingLevel(ResolveThinkingCapability(resolved.Definition.Capabilities), resolved.ThinkingLevel)
	}
	return resolved
}

// codexAcceptedThinkingLevels is the reasoning-effort enumeration the Codex API
// accepts. The /models catalog advertises more (`ultra`, a Codex-CLIENT feature
// for automatic task delegation) and the API 400s on it: live probe 2026-10-04,
// "Invalid value: 'ultra'. Supported values are: none, minimal, low, medium,
// high, xhigh, max".
var codexAcceptedThinkingLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// CodexAcceptedThinkingLevels returns a copy of the effort levels the Codex API
// accepts.
func CodexAcceptedThinkingLevels() []string {
	return slices.Clone(codexAcceptedThinkingLevels)
}

// IntersectCodexLevels keeps the advertised levels the Codex API accepts, in
// advertised order. Advertised levels are never trusted raw.
func IntersectCodexLevels(advertised []string) []string {
	var out []string
	for _, level := range advertised {
		level = strings.ToLower(strings.TrimSpace(level))
		if slices.Contains(codexAcceptedThinkingLevels, level) && !slices.Contains(out, level) {
			out = append(out, level)
		}
	}
	return out
}
