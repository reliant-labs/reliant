// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const (
	// Family is the driver family for standard Anthropic API
	Family models.Family = "anthropic"
	// ClaudeCodeFamily is the driver family for Claude Code API (sk-ant-oat keys)
	ClaudeCodeFamily models.Family = "claude-code"
)

// createAnthropicClient is the driver factory for standard Anthropic API
func createAnthropicClient(opts *llm.DriverOptions) (registry.Client, error) {
	// Route to Claude Code client if using sk-ant-oat key
	if IsClaudeCodeKey(opts.ApiKey) {
		return NewClaudeCodeClient(*opts), nil
	}
	return NewAnthropicClient(*opts), nil
}

// createClaudeCodeClient is the driver factory for Claude Code API
func createClaudeCodeClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClaudeCodeClient(*opts), nil
}

func init() {
	// Both families serve whatever models.yaml maps to `anthropic`. The Claude
	// Code family is the same Anthropic model set reached with an sk-ant-oat
	// subscription token rather than an API key, so it reads the same mappings
	// instead of keeping a second list that could fall behind.
	models.RegisterCatalogDriver(Family)
	// Register the Anthropic driver factory (auto-routes to Claude Code for sk-ant-oat keys)
	registry.RegisterDriver(Family, createAnthropicClient)

	// Register Claude Code as a separate driver family (explicit selection)
	models.RegisterCatalogDriverAs(ClaudeCodeFamily, Family)
	registry.RegisterDriver(ClaudeCodeFamily, createClaudeCodeClient)
}
