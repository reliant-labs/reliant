// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

// newTestClient returns a minimal GeminiClient suitable for unit tests that
// do not make network calls.
func newTestClient() *GeminiClient {
	return &GeminiClient{}
}

// makeToolMsg builds a message.Message with Tool role containing the given ToolResult parts.
func makeToolMsg(results ...message.ToolResult) message.Message {
	msg := message.Message{Role: message.Tool}
	for _, r := range results {
		msg.Parts = append(msg.Parts, r)
	}
	return msg
}

// makeAssistantMsg builds a message.Message with Assistant role containing a single ToolCall.
func makeAssistantMsg(call message.ToolCall) message.Message {
	msg := message.Message{Role: message.Assistant}
	msg.Parts = append(msg.Parts, call)
	return msg
}

// TestTextOnlyToolResult verifies that a plain text tool result produces exactly
// one genai.Content entry with one FunctionResponse part and no extra parts.
func TestTextOnlyToolResult(t *testing.T) {
	g := newTestClient()

	msgs := []message.Message{
		makeToolMsg(message.ToolResult{
			ToolCallID: "call_abc",
			Name:       "bash",
			Content:    "ok",
		}),
	}

	history := g.convertMessages(msgs)

	require.Len(t, history, 1)
	content := history[0]
	assert.Equal(t, "user", content.Role)
	require.Len(t, content.Parts, 1)

	part := content.Parts[0]
	require.NotNil(t, part.FunctionResponse)
	assert.Equal(t, "bash", part.FunctionResponse.Name)
	assert.Equal(t, map[string]interface{}{"result": "ok"}, part.FunctionResponse.Response)
	assert.Nil(t, part.InlineData, "text-only tool result must not have InlineData")
}

// TestToolResultWithImage verifies that a tool result carrying one image binary
// part produces a genai.Content with 2 parts: FunctionResponse + InlineData.
func TestToolResultWithImage(t *testing.T) {
	g := newTestClient()

	imgData := []byte{1, 2, 3, 4, 5}
	msgs := []message.Message{
		makeToolMsg(message.ToolResult{
			ToolCallID: "call_img",
			Name:       "screenshot",
			Content:    "done",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: imgData},
			},
		}),
	}

	history := g.convertMessages(msgs)

	require.Len(t, history, 1)
	content := history[0]
	assert.Equal(t, "user", content.Role)
	require.Len(t, content.Parts, 2, "expected FunctionResponse + InlineData")

	// Part 0: FunctionResponse
	assert.NotNil(t, content.Parts[0].FunctionResponse)
	assert.Equal(t, "screenshot", content.Parts[0].FunctionResponse.Name)

	// Part 1: InlineData – bytes must be raw, NOT base64-encoded
	require.NotNil(t, content.Parts[1].InlineData)
	assert.Equal(t, "image/png", content.Parts[1].InlineData.MIMEType)
	assert.Equal(t, imgData, content.Parts[1].InlineData.Data, "image data must be exact bytes, not base64")
}

// TestToolResultWithPDF verifies PDF binary parts are passed through correctly.
func TestToolResultWithPDF(t *testing.T) {
	g := newTestClient()

	pdfData := []byte("%PDF-1.4 fake pdf content")
	msgs := []message.Message{
		makeToolMsg(message.ToolResult{
			ToolCallID: "call_pdf",
			Name:       "read_file",
			Content:    "read",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "application/pdf", Data: pdfData},
			},
		}),
	}

	history := g.convertMessages(msgs)

	require.Len(t, history, 1)
	content := history[0]
	require.Len(t, content.Parts, 2)

	assert.NotNil(t, content.Parts[0].FunctionResponse)
	require.NotNil(t, content.Parts[1].InlineData)
	assert.Equal(t, "application/pdf", content.Parts[1].InlineData.MIMEType)
	assert.Equal(t, pdfData, content.Parts[1].InlineData.Data)
}

// TestToolResultWithMultipleBinaryParts verifies that 2 image binary parts produce
// a genai.Content with 3 parts: FunctionResponse + 2 InlineData parts.
func TestToolResultWithMultipleBinaryParts(t *testing.T) {
	g := newTestClient()

	img1 := []byte{10, 20, 30}
	img2 := []byte{40, 50, 60}
	msgs := []message.Message{
		makeToolMsg(message.ToolResult{
			ToolCallID: "call_multi",
			Name:       "capture",
			Content:    "captured",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: img1},
				{MIMEType: "image/jpeg", Data: img2},
			},
		}),
	}

	history := g.convertMessages(msgs)

	require.Len(t, history, 1)
	content := history[0]
	require.Len(t, content.Parts, 3, "expected FunctionResponse + 2 InlineData")

	assert.NotNil(t, content.Parts[0].FunctionResponse)

	require.NotNil(t, content.Parts[1].InlineData)
	assert.Equal(t, "image/png", content.Parts[1].InlineData.MIMEType)
	assert.Equal(t, img1, content.Parts[1].InlineData.Data)

	require.NotNil(t, content.Parts[2].InlineData)
	assert.Equal(t, "image/jpeg", content.Parts[2].InlineData.MIMEType)
	assert.Equal(t, img2, content.Parts[2].InlineData.Data)
}

// TestMultipleToolResults verifies that two ToolResult parts in a Tool message
// produce two separate genai.Content entries, each with "user" role.
func TestMultipleToolResults(t *testing.T) {
	g := newTestClient()

	msgs := []message.Message{
		makeToolMsg(
			message.ToolResult{ToolCallID: "call_1", Name: "bash", Content: "result1"},
			message.ToolResult{ToolCallID: "call_2", Name: "view", Content: "result2"},
		),
	}

	history := g.convertMessages(msgs)

	require.Len(t, history, 2, "each ToolResult should become its own genai.Content")

	assert.Equal(t, "user", history[0].Role)
	require.Len(t, history[0].Parts, 1)
	require.NotNil(t, history[0].Parts[0].FunctionResponse)
	assert.Equal(t, "bash", history[0].Parts[0].FunctionResponse.Name)

	assert.Equal(t, "user", history[1].Role)
	require.Len(t, history[1].Parts, 1)
	require.NotNil(t, history[1].Parts[0].FunctionResponse)
	assert.Equal(t, "view", history[1].Parts[0].FunctionResponse.Name)
}

// TestFunctionResponseNameIsToolName verifies that FunctionResponse.Name uses
// the tool's Name field, NOT the ToolCallID.
func TestFunctionResponseNameIsToolName(t *testing.T) {
	g := newTestClient()

	msgs := []message.Message{
		makeToolMsg(message.ToolResult{
			ToolCallID: "call_xyz_should_not_appear",
			Name:       "view",
			Content:    "file contents",
		}),
	}

	history := g.convertMessages(msgs)

	require.Len(t, history, 1)
	require.Len(t, history[0].Parts, 1)
	require.NotNil(t, history[0].Parts[0].FunctionResponse)
	assert.Equal(t, "view", history[0].Parts[0].FunctionResponse.Name,
		"FunctionResponse.Name must be the tool name, not the call ID")
	assert.NotEqual(t, "call_xyz_should_not_appear", history[0].Parts[0].FunctionResponse.Name)
}

// TestToolResultNameFallbackFromHistory verifies that when Name is empty on the
// ToolResult, the driver falls back to finding the name from the assistant message.
func TestToolResultNameFallbackFromHistory(t *testing.T) {
	g := newTestClient()

	assistantMsg := makeAssistantMsg(message.ToolCall{
		ID:    "call_fallback",
		Name:  "bash",
		Input: `{"cmd":"ls"}`,
	})

	toolMsg := makeToolMsg(message.ToolResult{
		ToolCallID: "call_fallback",
		Name:       "", // intentionally empty to trigger fallback
		Content:    "file1.txt",
	})

	history := g.convertMessages([]message.Message{assistantMsg, toolMsg})

	// Filter to only user-role entries (the function response)
	var userContents []*genai.Content
	for _, c := range history {
		if c.Role == "user" {
			userContents = append(userContents, c)
		}
	}

	require.Len(t, userContents, 1)
	require.NotNil(t, userContents[0].Parts[0].FunctionResponse)
	assert.Equal(t, "bash", userContents[0].Parts[0].FunctionResponse.Name)
}
