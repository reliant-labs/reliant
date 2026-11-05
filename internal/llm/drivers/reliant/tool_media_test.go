// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

func convertToWire(t *testing.T, msgs []message.Message) []map[string]any {
	t.Helper()
	client := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "claude-opus-4-8"}})
	data, err := json.Marshal(client.ConvertMessages(nil, msgs))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// The managed gateway speaks chat completions, whose tool messages hold text
// only. A turn's tool-result images therefore follow as ONE user message after
// all of its tool messages, as data URLs. The driver used to send the tool
// text alone, so the model never saw the screenshot it asked for.
func TestConvertMessages_ToolResultImagesFollowTheToolMessages(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	out := convertToWire(t, []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "call_view", Name: "view", Input: `{}`},
			message.ToolCall{ID: "call_bash", Name: "bash", Input: `{}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_view", Content: "Image file: shot.png",
				BinaryParts: []message.BinaryContent{{Path: "/w/shot.png", MIMEType: "image/png", Data: png}}},
			message.ToolResult{ToolCallID: "call_bash", Content: "ok"},
		}},
	})

	roles := make([]string, 0, len(out))
	for _, m := range out {
		roles = append(roles, m["role"].(string))
	}
	if got := strings.Join(roles, ","); got != "assistant,tool,tool,user" {
		t.Fatalf("roles = %s, want assistant,tool,tool,user", got)
	}
	follow, _ := json.Marshal(out[3])
	for _, want := range []string{
		"From tool call call_view:",
		`"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(png) + `"`,
	} {
		if !strings.Contains(string(follow), want) {
			t.Errorf("follow-up user message is missing %s: %s", want, follow)
		}
	}
}

// A user's attached image is sent as a data URL too. It used to be
// BinaryContent.String("reliant"), which is bare base64 — not a URL any
// OpenAI-compatible endpoint accepts.
func TestConvertMessages_UserImageIsADataURL(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G'}
	out := convertToWire(t, []message.Message{{Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: "what is this"},
		message.BinaryContent{MIMEType: "image/png", Data: png},
	}}})
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"url":"data:image/png;base64,`+base64.StdEncoding.EncodeToString(png)+`"`) {
		t.Fatalf("user image is not a data URL: %s", b)
	}
}
