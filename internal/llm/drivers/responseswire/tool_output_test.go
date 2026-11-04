// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

func marshalItem(t *testing.T, result message.ToolResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(FunctionCallOutputItem(result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var item map[string]any
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return item
}

func TestFunctionCallOutputItem_TextOnlyIsAString(t *testing.T) {
	t.Parallel()
	item := marshalItem(t, message.ToolResult{ToolCallID: "call_1", Content: "ok"})
	if item["type"] != "function_call_output" || item["call_id"] != "call_1" {
		t.Fatalf("item = %v, want a function_call_output for call_1", item)
	}
	if item["output"] != "ok" {
		t.Fatalf("output = %#v, want the plain string", item["output"])
	}
}

// The image rides inside the output as input_image, the PDF as input_file:
// the Responses API takes both natively.
func TestFunctionCallOutputItem_MediaRidesInTheOutput(t *testing.T) {
	t.Parallel()
	png := []byte{0x89, 'P', 'N', 'G'}
	pdf := []byte("%PDF-1.4")
	item := marshalItem(t, message.ToolResult{
		ToolCallID: "call_view",
		Content:    "Image file: shot.png (image/png, 241.0KB)",
		BinaryParts: []message.BinaryContent{
			{Path: "/w/shot.png", MIMEType: "image/png", Data: png},
			{Path: "/w/report.pdf", MIMEType: "application/pdf", Data: pdf},
		},
	})
	if item["call_id"] != "call_view" {
		t.Fatalf("call_id = %v, want call_view", item["call_id"])
	}
	output, ok := item["output"].([]any)
	if !ok || len(output) != 3 {
		t.Fatalf("output = %#v, want [input_text, input_image, input_file]", item["output"])
	}
	text := output[0].(map[string]any)
	if text["type"] != "input_text" || text["text"] != "Image file: shot.png (image/png, 241.0KB)" {
		t.Errorf("output[0] = %v, want the result's text", text)
	}
	img := output[1].(map[string]any)
	if img["type"] != "input_image" || img["image_url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png) {
		t.Errorf("output[1] = %v, want the PNG as a data URL", img)
	}
	file := output[2].(map[string]any)
	if file["type"] != "input_file" || file["filename"] != "report.pdf" ||
		file["file_data"] != "data:application/pdf;base64,"+base64.StdEncoding.EncodeToString(pdf) {
		t.Errorf("output[2] = %v, want the PDF as input_file", file)
	}
}
