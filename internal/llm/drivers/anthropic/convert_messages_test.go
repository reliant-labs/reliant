// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient returns an Anthropic client with cache disabled so tests don't
// need to reason about which block gets cache_control appended.
func newTestClient() *AnthropicClient {
	return NewAnthropicClient(llm.DriverOptions{
		Model: models.Model{
			ID:       models.Claude45Sonnet,
			APIModel: "claude-sonnet-4-5-20250929",
		},
		DisableCache: true,
		MaxTokens:    1024,
	})
}

// toolMsg builds a message.Tool message with the given ToolResult parts.
// It also inserts a preceding assistant message with matching tool_use IDs so
// that validateToolResultReferences doesn't strip the results.
func toolMsgWithAssistant(results ...message.ToolResult) []message.Message {
	// Build assistant message with one tool_use block per result
	assistantParts := make([]message.ContentPart, len(results))
	for i, r := range results {
		assistantParts[i] = message.ToolCall{
			ID:   r.ToolCallID,
			Name: r.Name,
		}
	}

	toolParts := make([]message.ContentPart, len(results))
	for i, r := range results {
		toolParts[i] = r
	}

	return []message.Message{
		{Role: message.Assistant, Parts: assistantParts},
		{Role: message.Tool, Parts: toolParts},
	}
}

// unmarshalMessages marshals each MessageParam to JSON then unmarshals into a
// slice of generic maps for easy field inspection.
func unmarshalMessages(t *testing.T, client *AnthropicClient, msgs []message.Message) []map[string]interface{} {
	t.Helper()
	converted := client.convertMessages(msgs)
	out := make([]map[string]interface{}, len(converted))
	for i, m := range converted {
		data, err := json.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &out[i]))
	}
	return out
}

// getToolResultBlock returns the tool_result content block at blockIdx inside
// the last message (the user message carrying tool results).
func getToolResultBlock(t *testing.T, msgs []map[string]interface{}, blockIdx int) map[string]interface{} {
	t.Helper()
	last := msgs[len(msgs)-1]
	content, ok := last["content"].([]interface{})
	require.True(t, ok, "last message content should be an array")
	require.Greater(t, len(content), blockIdx, "expected at least %d tool_result block(s)", blockIdx+1)
	block, ok := content[blockIdx].(map[string]interface{})
	require.True(t, ok)
	return block
}

// TestConvertMessages_TextOnlyToolResult verifies that a tool result with only
// text and no binary parts produces a tool_result block.
// The Anthropic SDK's NewToolResultBlock serializes content as a block array
// [{type:"text", text:"..."}] rather than a plain string — we assert on that shape.
func TestConvertMessages_TextOnlyToolResult(t *testing.T) {
	client := newTestClient()
	msgs := toolMsgWithAssistant(message.ToolResult{
		ToolCallID: "call_1",
		Name:       "my_tool",
		Content:    "hello",
		IsError:    false,
	})

	out := unmarshalMessages(t, client, msgs)
	block := getToolResultBlock(t, out, 0)

	assert.Equal(t, "tool_result", block["type"])
	assert.Equal(t, "call_1", block["tool_use_id"])

	// The SDK wraps content in a text block array.
	contentArr, ok := block["content"].([]interface{})
	require.True(t, ok, "content should be a block array")
	require.Len(t, contentArr, 1)
	textBlock := contentArr[0].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	assert.Equal(t, "hello", textBlock["text"])
}

// TestConvertMessages_TextOnlyErrorToolResult verifies is_error is propagated.
func TestConvertMessages_TextOnlyErrorToolResult(t *testing.T) {
	client := newTestClient()
	msgs := toolMsgWithAssistant(message.ToolResult{
		ToolCallID: "call_err",
		Name:       "my_tool",
		Content:    "error msg",
		IsError:    true,
	})

	out := unmarshalMessages(t, client, msgs)
	block := getToolResultBlock(t, out, 0)

	assert.Equal(t, "tool_result", block["type"])
	assert.Equal(t, true, block["is_error"])

	// SDK wraps text content as block array
	contentArr, ok := block["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, contentArr, 1)
	textBlock := contentArr[0].(map[string]interface{})
	assert.Equal(t, "error msg", textBlock["text"])
}

// TestConvertMessages_ToolResultWithSingleImage verifies that a tool result
// with one image BinaryPart produces a content array: [text block, image block].
func TestConvertMessages_ToolResultWithSingleImage(t *testing.T) {
	imgData := []byte{1, 2, 3}
	client := newTestClient()
	msgs := toolMsgWithAssistant(message.ToolResult{
		ToolCallID: "call_img",
		Name:       "screenshot",
		Content:    "here is the screenshot",
		BinaryParts: []message.BinaryContent{
			{MIMEType: "image/png", Data: imgData},
		},
	})

	out := unmarshalMessages(t, client, msgs)
	block := getToolResultBlock(t, out, 0)

	assert.Equal(t, "tool_result", block["type"])

	// content must be an array, not a string
	contentArr, ok := block["content"].([]interface{})
	require.True(t, ok, "content should be an array when binary parts are present")
	require.Len(t, contentArr, 2, "expected [text, image]")

	// First element: text block
	textBlock := contentArr[0].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	assert.Equal(t, "here is the screenshot", textBlock["text"])

	// Second element: image block
	imgBlock := contentArr[1].(map[string]interface{})
	assert.Equal(t, "image", imgBlock["type"])
	source := imgBlock["source"].(map[string]interface{})
	assert.Equal(t, "base64", source["type"])
	assert.Equal(t, "image/png", source["media_type"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(imgData), source["data"])
}

// TestConvertMessages_ToolResultWithPDF verifies PDF binary parts produce a
// document block inside the content array.
func TestConvertMessages_ToolResultWithPDF(t *testing.T) {
	pdfData := []byte{0x25, 0x50, 0x44, 0x46} // "%PDF" magic bytes
	client := newTestClient()
	msgs := toolMsgWithAssistant(message.ToolResult{
		ToolCallID: "call_pdf",
		Name:       "read_doc",
		Content:    "the document",
		BinaryParts: []message.BinaryContent{
			{MIMEType: "application/pdf", Data: pdfData},
		},
	})

	out := unmarshalMessages(t, client, msgs)
	block := getToolResultBlock(t, out, 0)

	contentArr, ok := block["content"].([]interface{})
	require.True(t, ok, "content should be an array")
	require.Len(t, contentArr, 2, "expected [text, document]")

	textBlock := contentArr[0].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])
	assert.Equal(t, "the document", textBlock["text"])

	docBlock := contentArr[1].(map[string]interface{})
	assert.Equal(t, "document", docBlock["type"])
	source := docBlock["source"].(map[string]interface{})
	assert.Equal(t, "base64", source["type"])
	assert.Equal(t, "application/pdf", source["media_type"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(pdfData), source["data"])
}

// TestConvertMessages_ToolResultNoTextOnlyImage verifies that when Content is
// empty, no empty text block is prepended to the content array.
func TestConvertMessages_ToolResultNoTextOnlyImage(t *testing.T) {
	imgData := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	client := newTestClient()
	msgs := toolMsgWithAssistant(message.ToolResult{
		ToolCallID: "call_notext",
		Name:       "capture",
		Content:    "", // empty
		BinaryParts: []message.BinaryContent{
			{MIMEType: "image/jpeg", Data: imgData},
		},
	})

	out := unmarshalMessages(t, client, msgs)
	block := getToolResultBlock(t, out, 0)

	contentArr, ok := block["content"].([]interface{})
	require.True(t, ok, "content should be an array")
	require.Len(t, contentArr, 1, "expected only the image block (no empty text block)")

	imgBlock := contentArr[0].(map[string]interface{})
	assert.Equal(t, "image", imgBlock["type"])
	source := imgBlock["source"].(map[string]interface{})
	assert.Equal(t, "image/jpeg", source["media_type"])
}

// TestConvertMessages_ToolResultMultipleImages verifies that multiple binary
// parts all appear as separate image blocks.
func TestConvertMessages_ToolResultMultipleImages(t *testing.T) {
	img1 := []byte{1, 1, 1}
	img2 := []byte{2, 2, 2}
	img3 := []byte{3, 3, 3}
	client := newTestClient()
	msgs := toolMsgWithAssistant(message.ToolResult{
		ToolCallID: "call_multi",
		Name:       "multi_shot",
		Content:    "three screenshots",
		BinaryParts: []message.BinaryContent{
			{MIMEType: "image/png", Data: img1},
			{MIMEType: "image/png", Data: img2},
			{MIMEType: "image/png", Data: img3},
		},
	})

	out := unmarshalMessages(t, client, msgs)
	block := getToolResultBlock(t, out, 0)

	contentArr, ok := block["content"].([]interface{})
	require.True(t, ok, "content should be an array")
	require.Len(t, contentArr, 4, "expected [text, img1, img2, img3]")

	textBlock := contentArr[0].(map[string]interface{})
	assert.Equal(t, "text", textBlock["type"])

	for i, raw := range contentArr[1:] {
		imgBlock := raw.(map[string]interface{})
		assert.Equal(t, "image", imgBlock["type"], "block %d should be image", i+1)
	}

	// Verify data integrity for each image
	expectedData := [][]byte{img1, img2, img3}
	for i, raw := range contentArr[1:] {
		imgBlock := raw.(map[string]interface{})
		source := imgBlock["source"].(map[string]interface{})
		assert.Equal(t, base64.StdEncoding.EncodeToString(expectedData[i]), source["data"], "image %d data mismatch", i)
	}
}

// TestConvertMessages_MultipleToolResults verifies that two ToolResult parts in
// one message produce two separate tool_result blocks.
func TestConvertMessages_MultipleToolResults(t *testing.T) {
	client := newTestClient()
	msgs := toolMsgWithAssistant(
		message.ToolResult{
			ToolCallID: "call_a",
			Name:       "tool_a",
			Content:    "result a",
		},
		message.ToolResult{
			ToolCallID: "call_b",
			Name:       "tool_b",
			Content:    "result b",
		},
	)

	out := unmarshalMessages(t, client, msgs)
	last := out[len(out)-1]
	content, ok := last["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, content, 2, "expected two tool_result blocks")

	blockA := content[0].(map[string]interface{})
	assert.Equal(t, "tool_result", blockA["type"])
	assert.Equal(t, "call_a", blockA["tool_use_id"])
	contentA := blockA["content"].([]interface{})
	assert.Equal(t, "result a", contentA[0].(map[string]interface{})["text"])

	blockB := content[1].(map[string]interface{})
	assert.Equal(t, "tool_result", blockB["type"])
	assert.Equal(t, "call_b", blockB["tool_use_id"])
	contentB := blockB["content"].([]interface{})
	assert.Equal(t, "result b", contentB[0].(map[string]interface{})["text"])
}

// TestConvertMessages_MixedTextAndImageToolResults tests two tool results in one
// message where the first is text-only and the second carries an image.
func TestConvertMessages_MixedTextAndImageToolResults(t *testing.T) {
	imgData := []byte{0xCA, 0xFE}
	client := newTestClient()
	msgs := toolMsgWithAssistant(
		message.ToolResult{
			ToolCallID: "call_text",
			Name:       "text_tool",
			Content:    "plain text result",
			// no BinaryParts
		},
		message.ToolResult{
			ToolCallID: "call_img",
			Name:       "img_tool",
			Content:    "image caption",
			BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: imgData},
			},
		},
	)

	out := unmarshalMessages(t, client, msgs)
	last := out[len(out)-1]
	content, ok := last["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, content, 2, "expected two tool_result blocks")

	// First result: text-only → SDK wraps in a text block array
	blockText := content[0].(map[string]interface{})
	assert.Equal(t, "tool_result", blockText["type"])
	assert.Equal(t, "call_text", blockText["tool_use_id"])
	textContent := blockText["content"].([]interface{})
	require.Len(t, textContent, 1, "text-only result should have one block")
	assert.Equal(t, "plain text result", textContent[0].(map[string]interface{})["text"])

	// Second result: has image → array content
	blockImg := content[1].(map[string]interface{})
	assert.Equal(t, "tool_result", blockImg["type"])
	assert.Equal(t, "call_img", blockImg["tool_use_id"])

	imgContent, ok := blockImg["content"].([]interface{})
	require.True(t, ok, "image-carrying result should have array content")
	require.Len(t, imgContent, 2, "expected [text, image]")

	textPart := imgContent[0].(map[string]interface{})
	assert.Equal(t, "text", textPart["type"])
	assert.Equal(t, "image caption", textPart["text"])

	imgPart := imgContent[1].(map[string]interface{})
	assert.Equal(t, "image", imgPart["type"])
	source := imgPart["source"].(map[string]interface{})
	assert.Equal(t, base64.StdEncoding.EncodeToString(imgData), source["data"])
}
