// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag/authortagtest"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestAuthorTag_GeminiSingleAuthorRequestUnchanged: a single-person chat sends the same request bytes through
// this driver whether or not its history went through author tagging
// (chat-collaboration.md §6.2), so its prompt cache survives the feature.
func TestAuthorTag_GeminiSingleAuthorRequestUnchanged(t *testing.T) {
	g := &GeminiClient{}
	authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
		msgs, _ := historyrepair.Apply(m, g.HistoryRules())
		return g.convertMessages(msgs)
	})
}
