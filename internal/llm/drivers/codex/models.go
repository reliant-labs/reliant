// Copyright (c) 2025 Reliant Labs
package codex

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family identifies this driver in the registry
const Family models.Family = "codex"

// SupportedModels lists the models that the Codex driver supports.
//
// This driver authenticates as a ChatGPT ACCOUNT, not an OpenAI API key, and
// the account backend serves only a subset of the GPT catalog. Everything else
// is refused with a 400 `{"detail":"The '<model>' model is not supported when
// using Codex with a ChatGPT account."}` — so listing a model here that the
// backend will not serve makes it resolvable and then fails every request.
//
// gpt-5.4, gpt-5.3-codex, gpt-5.3-codex-spark and gpt-5.2-codex were verified
// refused and are served by the openai/openrouter drivers instead. See the
// note above gpt-5.5 in models.yaml for the probe and the full list.
var SupportedModels = []models.ModelID{
	models.GPT6Astra,
	models.GPT56Sol,
	models.GPT56Luna,
	models.GPT56Terra,
	models.GPT55,
	models.GPT54Mini,
}

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts)
}

func init() {
	// Explicit list, NOT the catalog: a ChatGPT account serves less than the
	// catalog maps to `codex` (see the note on SupportedModels).
	models.RegisterDriverModels(Family, SupportedModels)
	// Register the driver factory
	registry.RegisterDriver(Family, createClient)
}
