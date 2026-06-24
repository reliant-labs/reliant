// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallLLMMessageInput(t *testing.T) {
	t.Run("supports tool_calls for assistant messages", func(t *testing.T) {
		// This tests that CallLLMMessageInput can represent an assistant message with tool calls
		msg := &reliantv1.CallLLMMessageInput{
			Role: "assistant",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{
					Id:    "call_123",
					Name:  "view",
					Input: `{"file_path": "/path/to/file.go"}`,
				},
				{
					Id:    "call_456",
					Name:  "grep",
					Input: `{"pattern": "func.*Test", "path": "."}`,
				},
			},
		}

		assert.Equal(t, "assistant", msg.GetRole())
		require.Len(t, msg.GetToolCalls(), 2)
		assert.Equal(t, "call_123", msg.GetToolCalls()[0].GetId())
		assert.Equal(t, "view", msg.GetToolCalls()[0].GetName())
		assert.Equal(t, "call_456", msg.GetToolCalls()[1].GetId())
		assert.Equal(t, "grep", msg.GetToolCalls()[1].GetName())
	})

	t.Run("supports user message with content", func(t *testing.T) {
		msg := &reliantv1.CallLLMMessageInput{
			Role:    "user",
			Content: "Filter these tool results:\n[{\"tool_call_id\": \"call_1\", \"content\": \"file contents\"}]",
		}

		assert.Equal(t, "user", msg.GetRole())
		assert.Contains(t, msg.GetContent(), "Filter these tool results")
	})

	t.Run("supports tool result message", func(t *testing.T) {
		msg := &reliantv1.CallLLMMessageInput{
			Role: "tool",
			ToolResult: &reliantv1.ToolResultMsg{
				ToolCallId: "call_123",
				Name:       "view",
				Content:    "file contents here",
				IsError:    false,
			},
		}

		assert.Equal(t, "tool", msg.GetRole())
		require.NotNil(t, msg.GetToolResult())
		assert.Equal(t, "call_123", msg.GetToolResult().GetToolCallId())
		assert.Equal(t, "view", msg.GetToolResult().GetName())
	})
}

func TestBuildInjectedMessages(t *testing.T) {
	t.Run("builds message with text content", func(t *testing.T) {
		input := &reliantv1.CallLLMMessageInput{
			Role:    "user",
			Content: "Hello, world!",
		}

		msg := message.Message{
			Role: message.MessageRole(input.GetRole()),
		}
		if input.GetContent() != "" {
			msg.Parts = []message.ContentPart{message.TextContent{Text: input.GetContent()}}
		}

		assert.Equal(t, message.User, msg.Role)
		require.Len(t, msg.Parts, 1)
		textPart, ok := msg.Parts[0].(message.TextContent)
		require.True(t, ok)
		assert.Equal(t, "Hello, world!", textPart.Text)
	})

	t.Run("builds message with tool calls", func(t *testing.T) {
		input := &reliantv1.CallLLMMessageInput{
			Role: "assistant",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "call_1", Name: "view", Input: "{}"},
				{Id: "call_2", Name: "grep", Input: "{}"},
			},
		}

		msg := message.Message{
			Role: message.MessageRole(input.GetRole()),
		}
		for _, tc := range input.GetToolCalls() {
			msg.Parts = append(msg.Parts, message.ToolCall{
				ID:    tc.GetId(),
				Name:  tc.GetName(),
				Input: tc.GetInput(),
			})
		}

		assert.Equal(t, message.Assistant, msg.Role)
		require.Len(t, msg.Parts, 2)

		tc1, ok := msg.Parts[0].(message.ToolCall)
		require.True(t, ok)
		assert.Equal(t, "call_1", tc1.ID)
		assert.Equal(t, "view", tc1.Name)

		tc2, ok := msg.Parts[1].(message.ToolCall)
		require.True(t, ok)
		assert.Equal(t, "call_2", tc2.ID)
		assert.Equal(t, "grep", tc2.Name)
	})

	t.Run("builds message with tool result", func(t *testing.T) {
		input := &reliantv1.CallLLMMessageInput{
			Role: "tool",
			ToolResult: &reliantv1.ToolResultMsg{
				ToolCallId: "call_1",
				Name:       "view",
				Content:    "result content",
				IsError:    false,
			},
		}

		msg := message.Message{
			Role: message.MessageRole(input.GetRole()),
		}
		if input.GetToolResult() != nil {
			tr := input.GetToolResult()
			msg.Parts = append(msg.Parts, message.ToolResult{
				ToolCallID: tr.GetToolCallId(),
				Name:       tr.GetName(),
				Content:    tr.GetContent(),
				IsError:    tr.GetIsError(),
			})
		}

		assert.Equal(t, message.Tool, msg.Role)
		require.Len(t, msg.Parts, 1)

		tr, ok := msg.Parts[0].(message.ToolResult)
		require.True(t, ok)
		assert.Equal(t, "call_1", tr.ToolCallID)
		assert.Equal(t, "result content", tr.Content)
	})

	t.Run("builds message with content and tool calls", func(t *testing.T) {
		// Some assistant messages might have both text content and tool calls
		input := &reliantv1.CallLLMMessageInput{
			Role:    "assistant",
			Content: "I'll help you with that. Let me check the file.",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "call_1", Name: "view", Input: `{"file_path": "/test.go"}`},
			},
		}

		msg := message.Message{
			Role: message.MessageRole(input.GetRole()),
		}
		if input.GetContent() != "" {
			msg.Parts = []message.ContentPart{message.TextContent{Text: input.GetContent()}}
		}
		for _, tc := range input.GetToolCalls() {
			msg.Parts = append(msg.Parts, message.ToolCall{
				ID:    tc.GetId(),
				Name:  tc.GetName(),
				Input: tc.GetInput(),
			})
		}

		assert.Equal(t, message.Assistant, msg.Role)
		require.Len(t, msg.Parts, 2)

		textPart, ok := msg.Parts[0].(message.TextContent)
		require.True(t, ok)
		assert.Contains(t, textPart.Text, "I'll help you")

		tcPart, ok := msg.Parts[1].(message.ToolCall)
		require.True(t, ok)
		assert.Equal(t, "view", tcPart.Name)
	})
}
