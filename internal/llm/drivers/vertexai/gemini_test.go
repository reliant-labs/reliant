// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

// newTestVertexAIClient returns a minimal VertexAIClient for unit tests that
// do not make network calls.
func newTestVertexAIClient() *VertexAIClient {
	return &VertexAIClient{}
}

// makeVertexToolMsg builds a message.Message with Tool role containing the given ToolResult parts.
func makeVertexToolMsg(results ...message.ToolResult) message.Message {
	msg := message.Message{Role: message.Tool}
	for _, r := range results {
		msg.Parts = append(msg.Parts, r)
	}
	return msg
}

// TestVertexAI_ToolCallIDBugFixed is the primary regression test: FunctionResponse.Name
// must be the tool's Name field, NOT the ToolCallID.
func TestVertexAI_ToolCallIDBugFixed(t *testing.T) {
	c := newTestVertexAIClient()

	msgs := []message.Message{
		makeVertexToolMsg(message.ToolResult{
			ToolCallID: "toolu_01abc_should_not_appear",
			Name:       "bash",
			Content:    "output",
		}),
	}

	contents := c.convertMessagesToGemini(msgs)

	require.Len(t, contents, 1)
	require.Len(t, contents[0].Parts, 1)
	require.NotNil(t, contents[0].Parts[0].FunctionResponse)
	assert.Equal(t, "bash", contents[0].Parts[0].FunctionResponse.Name,
		"FunctionResponse.Name must be the tool name, not the ToolCallID")
	assert.NotEqual(t, "toolu_01abc_should_not_appear", contents[0].Parts[0].FunctionResponse.Name,
		"ToolCallID must NOT be used as FunctionResponse.Name")
}

// TestVertexAI_TextOnlyToolResult verifies a plain text tool result produces
// exactly one genai.Content with one FunctionResponse part and no InlineData.
func TestVertexAI_TextOnlyToolResult(t *testing.T) {
	c := newTestVertexAIClient()

	msgs := []message.Message{
		makeVertexToolMsg(message.ToolResult{
			ToolCallID: "call_abc",
			Name:       "bash",
			Content:    "ok",
		}),
	}

	contents := c.convertMessagesToGemini(msgs)

	require.Len(t, contents, 1)
	content := contents[0]
	assert.Equal(t, string(genai.RoleUser), content.Role)
	require.Len(t, content.Parts, 1)
	require.NotNil(t, content.Parts[0].FunctionResponse)
	assert.Equal(t, "bash", content.Parts[0].FunctionResponse.Name)
	assert.Nil(t, content.Parts[0].InlineData)
}

// TestVertexAI_BinaryImagePart verifies that an image binary part is appended
// as an InlineData part alongside the FunctionResponse.
func TestVertexAI_BinaryImagePart(t *testing.T) {
	c := newTestVertexAIClient()

	imgData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	msgs := []message.Message{
		makeVertexToolMsg(message.ToolResult{
			ToolCallID: "call_img",
			Name:       "screenshot",
			Content:    "captured",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: imgData},
			},
		}),
	}

	contents := c.convertMessagesToGemini(msgs)

	require.Len(t, contents, 1)
	content := contents[0]
	assert.Equal(t, string(genai.RoleUser), content.Role)
	require.Len(t, content.Parts, 2, "expected FunctionResponse + InlineData")

	// Part 0: FunctionResponse
	require.NotNil(t, content.Parts[0].FunctionResponse)
	assert.Equal(t, "screenshot", content.Parts[0].FunctionResponse.Name)

	// Part 1: InlineData – raw bytes, not base64
	require.NotNil(t, content.Parts[1].InlineData)
	assert.Equal(t, "image/png", content.Parts[1].InlineData.MIMEType)
	assert.Equal(t, imgData, content.Parts[1].InlineData.Data, "image data must be exact bytes, not base64-encoded")
}

// TestVertexAI_PDFBinaryPart verifies PDF binary parts are passed through correctly.
func TestVertexAI_PDFBinaryPart(t *testing.T) {
	c := newTestVertexAIClient()

	pdfData := []byte("%PDF-1.4 fake content")
	msgs := []message.Message{
		makeVertexToolMsg(message.ToolResult{
			ToolCallID: "call_pdf",
			Name:       "read_file",
			Content:    "read",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "application/pdf", Data: pdfData},
			},
		}),
	}

	contents := c.convertMessagesToGemini(msgs)

	require.Len(t, contents, 1)
	content := contents[0]
	require.Len(t, content.Parts, 2)

	require.NotNil(t, content.Parts[0].FunctionResponse)
	require.NotNil(t, content.Parts[1].InlineData)
	assert.Equal(t, "application/pdf", content.Parts[1].InlineData.MIMEType)
	assert.Equal(t, pdfData, content.Parts[1].InlineData.Data)
}

// TestVertexAI_RoleIsUser verifies that all function response content blocks
// use the "user" role as required by the Gemini API.
func TestVertexAI_RoleIsUser(t *testing.T) {
	c := newTestVertexAIClient()

	msgs := []message.Message{
		makeVertexToolMsg(
			message.ToolResult{ToolCallID: "c1", Name: "bash", Content: "r1"},
			message.ToolResult{ToolCallID: "c2", Name: "view", Content: "r2"},
		),
	}

	contents := c.convertMessagesToGemini(msgs)

	require.Len(t, contents, 2)
	for i, content := range contents {
		assert.Equal(t, string(genai.RoleUser), content.Role,
			"content[%d] must have user role", i)
	}
}

// TestVertexAI_MultipleToolResults verifies that two ToolResult parts produce
// two separate genai.Content entries.
func TestVertexAI_MultipleToolResults(t *testing.T) {
	c := newTestVertexAIClient()

	msgs := []message.Message{
		makeVertexToolMsg(
			message.ToolResult{ToolCallID: "c1", Name: "bash", Content: "result1"},
			message.ToolResult{ToolCallID: "c2", Name: "view", Content: "result2"},
		),
	}

	contents := c.convertMessagesToGemini(msgs)

	require.Len(t, contents, 2, "each ToolResult must become its own genai.Content")

	require.NotNil(t, contents[0].Parts[0].FunctionResponse)
	assert.Equal(t, "bash", contents[0].Parts[0].FunctionResponse.Name)

	require.NotNil(t, contents[1].Parts[0].FunctionResponse)
	assert.Equal(t, "view", contents[1].Parts[0].FunctionResponse.Name)
}

// TestVertexAI_MultipleBinaryParts verifies 2 binary parts produce 3 content parts.
func TestVertexAI_MultipleBinaryParts(t *testing.T) {
	c := newTestVertexAIClient()

	img1 := []byte{1, 2, 3}
	img2 := []byte{4, 5, 6}
	msgs := []message.Message{
		makeVertexToolMsg(message.ToolResult{
			ToolCallID: "call_multi",
			Name:       "capture",
			Content:    "ok",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: img1},
				{MIMEType: "image/jpeg", Data: img2},
			},
		}),
	}

	contents := c.convertMessagesToGemini(msgs)

	require.Len(t, contents, 1)
	require.Len(t, contents[0].Parts, 3, "expected FunctionResponse + 2 InlineData parts")

	assert.NotNil(t, contents[0].Parts[0].FunctionResponse)

	require.NotNil(t, contents[0].Parts[1].InlineData)
	assert.Equal(t, "image/png", contents[0].Parts[1].InlineData.MIMEType)
	assert.Equal(t, img1, contents[0].Parts[1].InlineData.Data)

	require.NotNil(t, contents[0].Parts[2].InlineData)
	assert.Equal(t, "image/jpeg", contents[0].Parts[2].InlineData.MIMEType)
	assert.Equal(t, img2, contents[0].Parts[2].InlineData.Data)
}
