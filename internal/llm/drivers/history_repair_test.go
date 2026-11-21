// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// rulingClient is a capturingClient with history rules of its own.
type rulingClient struct {
	capturingClient
	rules historyrepair.Rules
}

func (c *rulingClient) HistoryRules() historyrepair.Rules { return c.rules }

// TestBaseDriver_RepairsHistoryBeforeEveryRequest: every production driver
// sends through baseDriver, so no provider client receives an orphaned tool
// result, an unanswered call, or an id its rules refuse, and the stored
// history is left as it was.
func TestBaseDriver_RepairsHistoryBeforeEveryRequest(t *testing.T) {
	history := func() []message.Message {
		return []message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}},
			{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "gone", Content: "stale"}}},
			{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "functions.read:0", Name: "read", Input: "{}"}}},
		}
	}
	stored := history()

	for name, client := range map[string]interface {
		registry.Client
		calls() [][]message.Message
	}{
		"default rules":   &capturing{},
		"anthropic rules": &ruling{rulingClient: rulingClient{rules: historyrepair.Rules{ToolCallIDs: historyrepair.AnthropicIDRules()}}},
	} {
		t.Run(name, func(t *testing.T) {
			d := &baseDriver{client: client, model: models.Model{ID: "m"}}
			for range d.StreamResponse(context.Background(), nil, stored, nil) { //nolint:revive // drain
			}
			_, err := d.SendMessages(context.Background(), nil, stored, nil)
			require.NoError(t, err)

			sent := client.calls()
			require.Len(t, sent, 2)
			for _, msgs := range sent {
				require.Len(t, msgs, 3, "the orphaned result is dropped and the call answered")
				require.Equal(t, message.Tool, msgs[2].Role)
				callID := msgs[1].ToolCalls()[0].ID
				require.Equal(t, callID, msgs[2].ToolResults()[0].ToolCallID)
				require.Equal(t, historyrepair.MissingToolResultContent, msgs[2].ToolResults()[0].Content)
				if name == "anthropic rules" {
					require.Regexp(t, `^[a-zA-Z0-9_-]+$`, callID)
				} else {
					require.Equal(t, "functions.read:0", callID)
				}
			}
			require.Equal(t, history(), stored, "the stored history must not change")
		})
	}
}

type capturing struct{ capturingClient }

func (c *capturing) calls() [][]message.Message { return c.sent }

type ruling struct{ rulingClient }

func (c *ruling) calls() [][]message.Message { return c.sent }
