// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// orgBoundReasoningDrivers are the driver families whose reasoning envelopes
// (encrypted_content) only the organization that produced them can verify; a
// foreign one is a hard 400. Anthropic is deliberately absent: its signatures
// are valid across accounts for most models, and for the account-bound ones
// the API drops the foreign block itself. Removing blocks from the middle of
// an Anthropic history would invalidate the blocks after them.
var orgBoundReasoningDrivers = map[models.DriverID]bool{
	"codex":  true,
	"openai": true,
}

// stripForeignReasoning removes provider-signed reasoning the target account
// cannot verify: for driver families whose envelopes are org-bound (codex,
// openai), every reasoning / redacted-reasoning part on an assistant message
// served by a different account of the same driver. Messages with no recorded
// account are kept (the driver-level safety net recovers those), as are text,
// tool calls and every other message.
//
// history is never modified: a message that changes is copied with a new Parts
// slice, so stored history stays exactly as saved.
func stripForeignReasoning(history []message.Message, driver models.DriverID, accountID string) []message.Message {
	if !orgBoundReasoningDrivers[driver] || accountID == "" {
		return history
	}
	var out []message.Message
	for i, m := range history {
		if m.Role != message.Assistant || m.ProviderAccountID == "" || m.ProviderAccountID == accountID {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		kept := make([]message.ContentPart, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch p.(type) {
			case message.ReasoningContent, message.RedactedReasoningContent:
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == len(m.Parts) {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		if out == nil {
			out = make([]message.Message, i, len(history))
			copy(out, history[:i])
		}
		m.Parts = kept
		out = append(out, m)
	}
	if out == nil {
		return history
	}
	return out
}
