// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family constant for Copilot
const Family models.Family = "copilot"

// IMPORTANT: the values below are INTERNAL model IDs (the `id:` field in
// models.yaml, dashed — e.g. "claude-4.8-opus"), NOT the per-provider
// `api_model` strings that Copilot's HTTP API expects (dotted — e.g.
// "claude-opus-4.8"). The dotted api_model is carried on each model's
// `copilot` provider mapping in models.yaml and reaches the driver at request
// time via opts.Model.APIModel. These two systems MUST agree: every internal
// ID registered here must have a `copilot` provider entry in models.yaml, so
// that CanDriverUseModel (this DriverMapping) and registry.Resolve (the YAML
// providers list) reach the same conclusion about what Copilot can serve.
//
// SupportedModels is the curated allowlist of models reachable through GitHub
// Copilot (single host api.individual.githubcopilot.com, gho_ Bearer auth). This
// is what the driver advertises to the model registry and MUST stay in agreement
// with the `copilot` provider entries in models.yaml.
//
// Each model is routed by the driver to the dialect its vendor speaks (claude-*
// -> Anthropic Messages /v1/messages; gpt-* -> OpenAI Chat /chat/completions).
// All three were confirmed to return 200 against the live endpoint with only the
// gho_ Bearer (no copilot-session-token) on a PAID Copilot plan. Models that GET
// /models reports policy=disabled for this account (e.g. claude-opus-4.8) are
// intentionally excluded — they 400 until the user enables them in GitHub Copilot
// settings; Phase 2 gates the picker on the per-account /models policy state.
var SupportedModels = []models.ModelID{
	models.GPT5Mini,      // "gpt-5-mini"     -> copilot api_model "gpt-5-mini"      (/chat/completions)
	models.Claude45Haiku, // "claude-4.5-haiku" -> copilot api_model "claude-haiku-4.5" (/v1/messages)
	models.Claude5Sonnet, // "claude-5-sonnet"  -> copilot api_model "claude-sonnet-5"  (/v1/messages)
}

// createClient is the driver factory function for the registry.
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts)
}

func init() {
	// Register which models this driver supports.
	models.RegisterDriverModels(Family, SupportedModels)
	// Register the driver factory so drivers.GetDriverForModel can construct it.
	registry.RegisterDriver(Family, createClient)
}
