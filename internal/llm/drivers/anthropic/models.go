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

// SupportedModels lists the models that both Anthropic drivers support
var SupportedModels = []models.ModelID{
	models.Claude45Haiku,
	models.Claude45Sonnet,
	models.Claude46Sonnet,
	models.Claude45Opus,
	models.Claude46Opus,
	models.Claude48Opus,
	models.Claude5Sonnet,
	models.Claude5Fable,
	models.Claude5Opus,
}

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
	// Register which models the Anthropic driver supports
	models.RegisterDriverModels(Family, SupportedModels)
	// Register the Anthropic driver factory (auto-routes to Claude Code for sk-ant-oat keys)
	registry.RegisterDriver(Family, createAnthropicClient)

	// Register Claude Code as a separate driver family (explicit selection)
	models.RegisterDriverModels(ClaudeCodeFamily, SupportedModels)
	registry.RegisterDriver(ClaudeCodeFamily, createClaudeCodeClient)
}
