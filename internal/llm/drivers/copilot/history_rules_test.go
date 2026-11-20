// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// TestCopilot_HistoryRulesFollowTheDialect: Copilot routes Claude to the
// Anthropic dialect and everything else to OpenAI's, and takes that dialect's
// history rules.
func TestCopilot_HistoryRulesFollowTheDialect(t *testing.T) {
	claude, err := NewClient(llm.DriverOptions{ApiKey: "k", Model: models.Model{ID: "claude-sonnet-4.5@copilot", APIModel: "claude-sonnet-4.5"}})
	require.NoError(t, err)
	require.Equal(t, historyrepair.AnthropicIDRules(), claude.HistoryRules().ToolCallIDs)

	gpt, err := NewClient(llm.DriverOptions{ApiKey: "k", Model: models.Model{ID: "gpt-4.1@copilot", APIModel: "gpt-4.1"}})
	require.NoError(t, err)
	require.NotZero(t, gpt.HistoryRules().ToolCallIDs.MaxLen)
}
