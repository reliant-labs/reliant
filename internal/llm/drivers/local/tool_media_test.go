// Copyright (c) 2025 Reliant Labs
package local

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// A local (OpenAI-compatible) model gets a turn's tool-result images as one
// user message after its tool messages; non-images are not sent, as on the
// user path.
func TestConvertMessages_ToolResultImagesFollowTheToolMessages(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G'}
	client := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "local-model"}})
	data, err := json.Marshal(client.ConvertMessages(nil, []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "call_view", Name: "view", Input: `{}`},
			message.ToolCall{ID: "call_pdf", Name: "view", Input: `{}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_view", Content: "Image file: shot.png",
				BinaryParts: []message.BinaryContent{{MIMEType: "image/png", Data: png}}},
			message.ToolResult{ToolCallID: "call_pdf", Content: "PDF file: r.pdf",
				BinaryParts: []message.BinaryContent{{MIMEType: "application/pdf", Data: []byte("%PDF")}}},
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	roles := make([]string, 0, len(out))
	for _, m := range out {
		roles = append(roles, m["role"].(string))
	}
	if got := strings.Join(roles, ","); got != "assistant,tool,tool,user" {
		t.Fatalf("roles = %s, want assistant,tool,tool,user", got)
	}
	follow, _ := json.Marshal(out[3])
	if !strings.Contains(string(follow), `"url":"data:image/png;base64,`+base64.StdEncoding.EncodeToString(png)+`"`) {
		t.Errorf("follow-up is missing the PNG data URL: %s", follow)
	}
	if strings.Contains(string(follow), "application/pdf") || strings.Contains(string(follow), "call_pdf") {
		t.Errorf("follow-up carries a non-image: %s", follow)
	}
}
