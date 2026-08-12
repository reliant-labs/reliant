// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// A System-role message in the middle of a conversation is real content the
// model must see: compaction summaries, branch notes, and the agent mailbox
// envelope all take this path. Both Vertex converters used to switch only on
// User/Assistant/Tool, so a System message fell through and was dropped.
func TestConvertMessagesToGemini_KeepsSystemHistoryMessage(t *testing.T) {
	c := newTestVertexAIClient()

	converted := c.convertMessagesToGemini([]message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first"}}},
		{Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "mid-history system note"}}},
	})

	b, err := json.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal contents: %v", err)
	}
	if !strings.Contains(string(b), "mid-history system note") {
		t.Fatalf("system history message was dropped from gemini payload: %s", b)
	}
}

func TestConvertMessagesToClaude_KeepsSystemHistoryMessage(t *testing.T) {
	c := newTestVertexAIClient()

	converted := c.convertMessagesToClaude([]message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first"}}},
		{Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "mid-history system note"}}},
	})

	b, err := json.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	if !strings.Contains(string(b), "mid-history system note") {
		t.Fatalf("system history message was dropped from claude payload: %s", b)
	}
}
