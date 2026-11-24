// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag/authortagtest"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestAuthorTag_AntigravitySingleAuthorRequestUnchanged: a single-person chat sends the same request bytes through
// this driver whether or not its history went through author tagging
// (chat-collaboration.md §6.2), so its prompt cache survives the feature.
func TestAuthorTag_AntigravitySingleAuthorRequestUnchanged(t *testing.T) {
	c := &Client{}
	authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
		msgs, _ := historyrepair.Apply(m, c.HistoryRules())
		return convertMessages(msgs)
	})
}
