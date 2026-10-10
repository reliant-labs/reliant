// Copyright (c) 2025 Reliant Labs
package models

import "slices"

// substituteTiers are the tiers a model is replaced within, most capable
// first. A model listed in several tiers is replaced by the pick of the most
// capable one, so a substitution never downgrades past what the user chose.
// meta is internal plumbing (titles, compaction), never a user's choice.
var substituteTiers = []string{TagPowerful, TagFlagship, TagReasoning, TagModerate, TagFast, TagCheap}

// Substitute names what the user's servable providers offer in place of a
// model none of them can serve: the same model on another provider when one
// serves it, else the pick of the most capable tier that lists the model —
// what a selector for that tier resolves to for this user. ok is false when
// neither exists (no servable provider answers the model or any of its
// tiers), and then nothing should be substituted.
//
// availableProviders must already exclude every provider that cannot serve
// the account (AvailableDrivers.Drivers does), and the registry should carry
// the account's availability (WithAvailability), so the substitute is one the
// next request can actually reach.
func (r *ModelRegistry) Substitute(modelID string, availableProviders []string) (*ResolvedModel, bool) {
	if _, ok := r.byID[modelID]; !ok {
		return nil, false
	}
	if resolved, err := r.Resolve(ModelSelector{ID: modelID}, availableProviders); err == nil {
		return resolved, true
	}
	tags := r.TagsOf(modelID)
	for _, tier := range substituteTiers {
		if !slices.Contains(tags, tier) {
			continue
		}
		if resolved, err := r.Resolve(ModelSelector{Tags: []string{tier}}, availableProviders); err == nil {
			return resolved, true
		}
	}
	return nil, false
}
