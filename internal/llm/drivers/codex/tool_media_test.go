// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// A view tool's screenshot reaches a Codex model inside its function_call
// output as input_image. The driver used to send only the result's text, so
// the model read "Image file: … (image/png, 241.0KB)" and never the image.
func TestConvertMessages_ToolResultImageRidesInTheFunctionCallOutput(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	wire := marshalItems(t, []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "call_view", Name: "view", Input: `{"file_path":"shot.png"}`},
			message.ToolCall{ID: "call_bash", Name: "bash", Input: `{"command":"ls"}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_view", Content: "Image file: shot.png (image/png, 6B)",
				BinaryParts: []message.BinaryContent{{Path: "/w/shot.png", MIMEType: "image/png", Data: png}}},
			message.ToolResult{ToolCallID: "call_bash", Content: "shot.png"},
		}},
	})

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	for _, want := range []string{
		`"output":[{"text":"Image file: shot.png (image/png, 6B)","type":"input_text"},{"image_url":"` + dataURL + `","detail":"auto","type":"input_image"}],"call_id":"call_view"`,
		`"output":"shot.png","call_id":"call_bash"`,
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("wire is missing %s\nwire: %s", want, wire)
		}
	}
}
