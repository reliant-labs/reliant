// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag/authortagtest"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestAuthorTag_ReliantSingleAuthorRequestUnchanged: a single-person chat sends the same request bytes through
// this driver whether or not its history went through author tagging
// (chat-collaboration.md §6.2), so its prompt cache survives the feature.
func TestAuthorTag_ReliantSingleAuthorRequestUnchanged(t *testing.T) {
	c := NewClient(llm.DriverOptions{ApiKey: "rlk-test", Model: models.Model{ID: "claude-opus-5"}})
	authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
		msgs, _ := historyrepair.Apply(m, c.HistoryRules())
		return c.ConvertMessages([]string{"You are a coding agent."}, msgs)
	})
}
