// Copyright (c) 2025 Reliant Labs
package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// newTestClient returns a minimal OpenaiClient for unit tests (no real API calls).
func newTestClient() *OpenaiClient {
	return NewClient(llm.DriverOptions{
		Model: models.Model{APIModel: "gpt-4o"},
	})
}

// toolMsg builds a message.Message with role=Tool containing the given ToolResults.
func toolMsg(results ...message.ToolResult) message.Message {
	parts := make([]message.ContentPart, len(results))
	for i, r := range results {
		parts[i] = r
	}
	return message.Message{Role: message.Tool, Parts: parts}
}

// marshalMessages marshals ConvertMessages output to a generic JSON structure for inspection.
func marshalMessages(t *testing.T, client *OpenaiClient, msgs []message.Message) []map[string]interface{} {
	t.Helper()
	converted := client.ConvertMessages(nil, msgs)
	b, err := json.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var result []map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return result
}

// roleOf extracts the "role" field from a marshalled message map.
func roleOf(t *testing.T, m map[string]interface{}) string {
	t.Helper()
	role, ok := m["role"].(string)
	if !ok {
		t.Fatalf("message has no string 'role': %v", m)
	}
	return role
}

// TestBinaryToolResult_TextOnly verifies that a tool result with no BinaryParts
// emits exactly one "tool" role message with string content (regression test).
func TestBinaryToolResult_TextOnly(t *testing.T) {
	client := newTestClient()
	msgs := marshalMessages(t, client, []message.Message{
		toolMsg(message.ToolResult{
			ToolCallID: "call-1",
			Name:       "my_tool",
			Content:    "tool output text",
		}),
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if role := roleOf(t, msgs[0]); role != "tool" {
		t.Fatalf("expected role 'tool', got %q", role)
	}
	b, _ := json.Marshal(msgs[0])
	if !strings.Contains(string(b), "tool output text") {
		t.Fatalf("expected content to contain tool output text, got: %s", b)
	}
}

// TestBinaryToolResult_SinglePNGImage verifies that a tool result with one PNG
// BinaryPart emits two messages: tool role then user role with image_url.
func TestBinaryToolResult_SinglePNGImage(t *testing.T) {
	imgData := []byte{1, 2, 3, 4, 5}
	client := newTestClient()
	msgs := marshalMessages(t, client, []message.Message{
		toolMsg(message.ToolResult{
			ToolCallID: "call-1",
			Name:       "screenshot",
			Content:    "here is the screenshot",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: imgData},
			},
		}),
	})

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// First: tool message
	if role := roleOf(t, msgs[0]); role != "tool" {
		t.Fatalf("first message role = %q, want 'tool'", role)
	}

	// Second: user message with image
	if role := roleOf(t, msgs[1]); role != "user" {
		t.Fatalf("second message role = %q, want 'user'", role)
	}

	b, _ := json.Marshal(msgs[1])
	jsonStr := string(b)

	if !strings.Contains(jsonStr, "Tool result attachments:") {
		t.Fatalf("expected 'Tool result attachments:' text in follow-up user message, got: %s", jsonStr)
	}

	expectedBase64 := base64.StdEncoding.EncodeToString(imgData)
	expectedURI := "data:image/png;base64," + expectedBase64
	if !strings.Contains(jsonStr, expectedURI) {
		t.Fatalf("expected data URI %q in follow-up user message, got: %s", expectedURI, jsonStr)
	}
}

// TestBinaryToolResult_JPEGImage verifies the data URI uses the correct JPEG MIME prefix.
func TestBinaryToolResult_JPEGImage(t *testing.T) {
	imgData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	client := newTestClient()
	msgs := marshalMessages(t, client, []message.Message{
		toolMsg(message.ToolResult{
			ToolCallID: "call-jpeg",
			Name:       "photo",
			Content:    "jpeg image",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/jpeg", Data: imgData},
			},
		}),
	})

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	b, _ := json.Marshal(msgs[1])
	jsonStr := string(b)

	expectedBase64 := base64.StdEncoding.EncodeToString(imgData)
	expectedURI := "data:image/jpeg;base64," + expectedBase64
	if !strings.Contains(jsonStr, expectedURI) {
		t.Fatalf("expected JPEG data URI in follow-up message, got: %s", jsonStr)
	}
	if strings.Contains(jsonStr, "data:image/png") {
		t.Fatalf("unexpected 'image/png' in JPEG test result: %s", jsonStr)
	}
}

// TestBinaryToolResult_PDFIncluded verifies that a PDF binary part is included
// as a file content part in the follow-up user message.
func TestBinaryToolResult_PDFIncluded(t *testing.T) {
	pdfData := []byte{0x25, 0x50, 0x44, 0x46}
	client := newTestClient()
	msgs := marshalMessages(t, client, []message.Message{
		toolMsg(message.ToolResult{
			ToolCallID: "call-pdf",
			Name:       "read_doc",
			Content:    "document content",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "application/pdf", Data: pdfData, Path: "/tmp/report.pdf"},
			},
		}),
	})

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// First: tool message
	if role := roleOf(t, msgs[0]); role != "tool" {
		t.Fatalf("first message role = %q, want 'tool'", role)
	}

	// Second: user message with file content part
	if role := roleOf(t, msgs[1]); role != "user" {
		t.Fatalf("second message role = %q, want 'user'", role)
	}

	b, _ := json.Marshal(msgs[1])
	jsonStr := string(b)

	expectedBase64 := base64.StdEncoding.EncodeToString(pdfData)
	expectedURI := "data:application/pdf;base64," + expectedBase64
	if !strings.Contains(jsonStr, expectedURI) {
		t.Fatalf("expected PDF data URI %q in follow-up user message, got: %s", expectedURI, jsonStr)
	}
	if !strings.Contains(jsonStr, "report.pdf") {
		t.Fatalf("expected filename 'report.pdf' in follow-up user message, got: %s", jsonStr)
	}
	if strings.Contains(jsonStr, "image_url") {
		t.Fatalf("PDF should not produce image_url content, got: %s", jsonStr)
	}
}

// TestBinaryToolResult_MultipleImages verifies that two image parts produce a
// follow-up user message with exactly two image_url entries.
func TestBinaryToolResult_MultipleImages(t *testing.T) {
	client := newTestClient()
	msgs := marshalMessages(t, client, []message.Message{
		toolMsg(message.ToolResult{
			ToolCallID: "call-multi",
			Name:       "capture",
			Content:    "two screenshots",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: []byte{1, 2, 3}},
				{MIMEType: "image/jpeg", Data: []byte{4, 5, 6}},
			},
		}),
	})

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	b, _ := json.Marshal(msgs[1])
	jsonStr := string(b)

	pngURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	jpegURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte{4, 5, 6})

	if !strings.Contains(jsonStr, pngURI) {
		t.Fatalf("expected PNG data URI in follow-up message, got: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, jpegURI) {
		t.Fatalf("expected JPEG data URI in follow-up message, got: %s", jsonStr)
	}

	// Count distinct image_url type entries: "type":"image_url" appears once per image part.
	count := strings.Count(jsonStr, `"type":"image_url"`)
	if count != 2 {
		t.Fatalf("expected 2 image_url type entries, got %d in: %s", count, jsonStr)
	}
}

// TestBinaryToolResult_MultipleToolResults_Mixed verifies: first result with images
// produces a follow-up user message; second result without images does not.
func TestBinaryToolResult_MultipleToolResults_Mixed(t *testing.T) {
	client := newTestClient()
	msgs := marshalMessages(t, client, []message.Message{
		toolMsg(
			message.ToolResult{
				ToolCallID: "call-a",
				Name:       "with_image",
				Content:    "has image",
				BinaryParts: []message.BinaryContent{
					{MIMEType: "image/png", Data: []byte{0xAB, 0xCD}},
				},
			},
			message.ToolResult{
				ToolCallID: "call-b",
				Name:       "text_only",
				Content:    "no image",
			},
		),
	})

	// Expected: tool(call-a), user(image follow-up), tool(call-b)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(msgs), msgs)
	}

	if role := roleOf(t, msgs[0]); role != "tool" {
		t.Fatalf("msgs[0] role = %q, want 'tool'", role)
	}
	if role := roleOf(t, msgs[1]); role != "user" {
		t.Fatalf("msgs[1] role = %q, want 'user' (image follow-up)", role)
	}
	if role := roleOf(t, msgs[2]); role != "tool" {
		t.Fatalf("msgs[2] role = %q, want 'tool'", role)
	}

	// Verify the follow-up contains the image from call-a only
	b, _ := json.Marshal(msgs[1])
	pngURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{0xAB, 0xCD})
	if !strings.Contains(string(b), pngURI) {
		t.Fatalf("expected PNG URI from call-a in follow-up message, got: %s", b)
	}
}

// TestBinaryToolResult_Base64Correctness verifies the exact base64 encoding of a
// known byte slice appears verbatim in the data URI.
func TestBinaryToolResult_Base64Correctness(t *testing.T) {
	knownBytes := []byte{0xFF, 0xD8, 0xFF}
	expectedBase64 := base64.StdEncoding.EncodeToString(knownBytes) // "/9j/"
	expectedURI := "data:image/jpeg;base64," + expectedBase64

	client := newTestClient()
	msgs := marshalMessages(t, client, []message.Message{
		toolMsg(message.ToolResult{
			ToolCallID: "call-b64",
			Name:       "check_encoding",
			Content:    "encoding test",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/jpeg", Data: knownBytes},
			},
		}),
	})

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	b, _ := json.Marshal(msgs[1])
	jsonStr := string(b)

	if !strings.Contains(jsonStr, expectedURI) {
		t.Fatalf("expected exact URI %q in follow-up message, got: %s", expectedURI, jsonStr)
	}
}
