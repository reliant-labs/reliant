// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag/authortagtest"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestAuthorTag_AnthropicSingleAuthorRequestUnchanged: a single-person chat sends the same request bytes through
// this driver whether or not its history went through author tagging
// (chat-collaboration.md §6.2), so its prompt cache survives the feature.
func TestAuthorTag_AnthropicSingleAuthorRequestUnchanged(t *testing.T) {
	c := newTestClient()
	authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
		msgs, _ := historyrepair.Apply(m, c.HistoryRules())
		return c.convertMessages(msgs)
	})
	cc := NewClaudeCodeClient(c.options)
	authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
		msgs, _ := historyrepair.Apply(m, cc.HistoryRules())
		return cc.convertMessages(msgs)
	})
}
