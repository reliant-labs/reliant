// Copyright (c) 2025 Reliant Labs
package models

import (
	"slices"
	"strings"
)

// ModelAvailability is what a credentialed provider reports about one model for
// the connected account: whether it may be used at all, why not, and the
// context limit the provider advertises for it.
type ModelAvailability struct {
	// Disabled means the account may not use the model on this provider.
	Disabled bool
	// Reason is the user-facing explanation for Disabled.
	Reason string
	// ContextWindow, when positive, is the context limit the provider
	// advertises for this account (codex: /codex/models max_context_window;
	// copilot: /models max_context_window_tokens). Whether it counts output is
	// not stated, so it never RAISES the prompt ceiling derived from the
	// catalog; when it is lower, it is the ceiling (ProviderPromptCeiling).
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

// resolvedWithAvailability builds the ResolvedModel, stamping the account's
// advertised context limit onto the chosen provider so ProviderPromptCeiling
// (compaction threshold, pin cap, trim backstop) never exceeds what the
// provider says it accepts.
func resolvedWithAvailability(model *ModelDefinition, provider *ProviderMapping, level string, avail AvailabilityFunc) *ResolvedModel {
	resolved := &ResolvedModel{Definition: *model, Provider: *provider, ThinkingLevel: level}
	if avail == nil {
		return resolved
	}
	a := avail(provider.Driver, model.ID)
	if a.ContextWindow > 0 {
		resolved.Provider.AdvertisedLimit = a.ContextWindow
		resolved.Definition.Providers = slices.Clone(model.Providers)
		for i := range resolved.Definition.Providers {
			if resolved.Definition.Providers[i].Driver == provider.Driver {
				resolved.Definition.Providers[i].AdvertisedLimit = a.ContextWindow
			}
		}
		logAdvertisedLimit(model, provider.Driver, a.ContextWindow)
	}
	if len(a.ThinkingLevels) > 0 {
		resolved.Definition.Capabilities.ThinkingLevels = slices.Clone(a.ThinkingLevels)
		resolved.ThinkingLevel = ClampThinkingLevel(ResolveThinkingCapability(resolved.Definition.Capabilities), resolved.ThinkingLevel)
	}
	return resolved
}

// ServedDefinition returns def as this registry's account is served it by
// providerDriver: the provider's advertised limit stamped on exactly as Resolve
// does, so a picker's ProviderPromptCeiling agrees with the request path. A
// provider the model does not map returns def unchanged.
func (r *ModelRegistry) ServedDefinition(def *ModelDefinition, providerDriver string) ModelDefinition {
	for i := range def.Providers {
		if def.Providers[i].Driver == providerDriver {
			return resolvedWithAvailability(def, &def.Providers[i], "", r.avail).Definition
		}
	}
	return *def
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
