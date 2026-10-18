// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const (
	// Family is the driver family for the metered Anthropic API (an API key).
	Family models.Family = "anthropic"
	// ClaudeFamily is the driver family for a Claude subscription (Claude
	// Code OAuth). It is the one id the subscription goes by everywhere: the
	// stored provider, the DriverID, the models.yaml mapping, a
	// `<model>@claude` pin and the client's Name().
	ClaudeFamily models.Family = "claude"
)

// createAnthropicClient is the driver factory for the Anthropic API.
func createAnthropicClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewAnthropicClient(*opts), nil
}

// createClaudeCodeClient is the driver factory for a Claude subscription.
func createClaudeCodeClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClaudeCodeClient(*opts), nil
}

func init() {
	models.RegisterCatalogDriver(Family)
	registry.RegisterDriver(Family, createAnthropicClient)

	models.RegisterCatalogDriver(ClaudeFamily)
	registry.RegisterDriver(ClaudeFamily, createClaudeCodeClient)
}
