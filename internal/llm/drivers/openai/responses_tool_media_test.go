// Copyright (c) 2025 Reliant Labs
package openai

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// On the Responses path a tool result's screenshot rides inside its
// function_call_output as input_image. It used to send only the text.
func TestConvertMessagesToResponsesInput_ToolResultImageRidesInTheOutput(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	wire := marshalResponsesInput(t, []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "call_view", Name: "view", Input: `{"file_path":"shot.png"}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_view", Content: "Image file: shot.png",
				BinaryParts: []message.BinaryContent{{Path: "/w/shot.png", MIMEType: "image/png", Data: png}}},
		}},
	})

	want := `"output":[{"text":"Image file: shot.png","type":"input_text"},{"image_url":"data:image/png;base64,` +
		base64.StdEncoding.EncodeToString(png) + `","detail":"auto","type":"input_image"}],"call_id":"call_view"`
	if !strings.Contains(wire, want) {
		t.Fatalf("wire is missing %s\nwire: %s", want, wire)
	}
}
