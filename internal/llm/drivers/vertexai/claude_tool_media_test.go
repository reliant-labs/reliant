// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// Claude on Vertex takes images and PDFs inside the tool_result, as the
// direct Anthropic driver sends them. This converter used to send the text
// alone.
func TestConvertMessagesToClaude_ToolResultCarriesItsMedia(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G'}
	pdf := []byte("%PDF-1.4")
	out := newTestVertexAIClient().convertMessagesToClaude([]message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "toolu_view", Name: "view", Input: `{}`},
			message.ToolCall{ID: "toolu_bash", Name: "bash", Input: `{}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "toolu_view", Content: "Image file: shot.png", BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: png},
				{MIMEType: "application/pdf", Data: pdf},
			}},
			message.ToolResult{ToolCallID: "toolu_bash", Content: "ok"},
		}},
	})
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(data)
	for _, want := range []string{
		`"tool_use_id":"toolu_view","content":[{"type":"text","text":"Image file: shot.png"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(png) + `"}},` +
			`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + base64.StdEncoding.EncodeToString(pdf) + `"}}]`,
		`"tool_use_id":"toolu_bash","content":"ok"`,
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("wire is missing %s\nwire: %s", want, wire)
		}
	}
}
