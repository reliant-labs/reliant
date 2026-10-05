// Copyright (c) 2025 Reliant Labs
package models

import "testing"

// copilotAccountLevels is the reasoning_effort list GitHub Copilot's /models
// reported for the probed individual-plan account (2026-10-04), keyed by the
// api_model Reliant maps. A model's declared thinking_levels must be servable.
var copilotAccountLevels = map[string][]string{
	"claude-sonnet-5.5":  {"low", "medium", "high", "xhigh", "max"},
	"claude-sonnet-5":    {"low", "medium", "high", "xhigh", "max"},
	"gemini-3.7-flash":   {"low", "medium", "high"},
	"gemini-3.8-flash":   {"low", "medium", "high"},
	"gpt-5.3-codex":      {"low", "medium", "high", "xhigh"},
	"gpt-5.4":            {"none", "low", "medium", "high", "xhigh"},
	"gpt-5.4-mini":       {"none", "low", "medium", "high", "xhigh"},
	"gpt-5.6-luna":       {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-terra":      {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-6-luna":         {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5-mini":         {"low", "medium", "high"},
	"grok-4.5":           {"low", "medium", "high"},
	"grok-4.6":           {"low", "medium", "high", "xhigh"},
	"grok-4.7":           {"low", "medium", "high", "xhigh"},
	"kimi-k3":            {"low", "high", "max"},
	"mai-code-1.1-flash": {"low", "medium", "high"},
	"gpt-5.4-nano":       {"none", "low", "medium", "high", "xhigh"},
}

// copilotDisabledOnIndividual are catalog api_models the individual plan reports
// policy=disabled; mapping one offers a picker option that 400s upstream.
var copilotDisabledOnIndividual = []string{
	"claude-opus-5.5", "claude-opus-5", "claude-opus-4.8", "claude-fable-5.1",
	"claude-fable-5", "gpt-5.5", "gpt-5.6-sol", "gpt-6-sol", "gpt-6.1-sol", "gpt-6-astra",
}

func TestCopilotMappingsAreServableOnTheProbedAccount(t *testing.T) {
	reg := MustGetRegistry()
	mapped := map[string]bool{}
	for _, def := range reg.GetUserVisibleModels() {
		for _, p := range def.Providers {
			if p.Driver != "copilot" {
				continue
			}
			mapped[p.APIModel] = true
			if p.APIModel == "claude-haiku-4.5" {
				continue // budget-tier Claude: /models lists no reasoning_effort
			}
			reported, ok := copilotAccountLevels[p.APIModel]
			if !ok {
				t.Errorf("%s: copilot api_model %q has no recorded /models levels; add it to copilotAccountLevels", def.ID, p.APIModel)
				continue
			}
			have := map[string]bool{}
			for _, l := range reported {
				have[l] = true
			}
			for _, l := range SupportedThinkingLevels(def.Capabilities) {
				if !have[l] {
					t.Errorf("%s@copilot: declared level %q is not served by Copilot (%v)", def.ID, l, reported)
				}
			}
		}
	}
	for api := range copilotAccountLevels {
		if !mapped[api] {
			t.Errorf("copilot api_model %q is policy=enabled but not mapped in models.yaml", api)
		}
	}
	for _, api := range copilotDisabledOnIndividual {
		if mapped[api] {
			t.Errorf("copilot api_model %q is policy=disabled on the individual plan and must not be mapped", api)
		}
	}
}
