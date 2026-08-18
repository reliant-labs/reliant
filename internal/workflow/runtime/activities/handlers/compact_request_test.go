// Copyright (c) 2025 Reliant Labs
// Regression tests for the compaction summarization request envelope.
//
// Compaction used to special-case its LLM request (reasoning explicitly
// disabled, raw tool-bearing history with no tools param) which produced
// provider 400s on both the direct anthropic driver ("clear_thinking_20251015
// strategy requires thinking to be enabled or adaptive") and the managed
// reliant driver via LiteLLM ("Anthropic doesn't support tool calling without
// tools= param"). These tests pin the fix: the request now flows through the
// same request-construction path as a normal CallLLM turn.
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingCompactionDriver records the request the compaction summarization
// call actually streams to the provider.
type capturingCompactionDriver struct {
	mockLLMDriverForIdempotency
	model       models.Model
	gotPrompts  []string
	gotMessages []message.Message
	gotTools    []tools.Tool
}

func (d *capturingCompactionDriver) Model() models.Model {
	return d.model
}

func (d *capturingCompactionDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	d.gotPrompts = prompts
	d.gotMessages = messages
	d.gotTools = tls
	return d.mockLLMDriverForIdempotency.StreamResponse(ctx, prompts, messages, tls)
}

// capturingCompactionResolver returns a DriverResolver that records the
// preferences and driver options of the final (non-probe) resolution call.
func capturingCompactionResolver(driver *capturingCompactionDriver, captured *capturedDriverOptions) drivers.DriverResolver {
	return func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		if prefs != nil {
			captured.Preferences = append(models.Preferences(nil), prefs...)
			driverOpts := llm.DriverOptions{}
			for _, opt := range opts {
				opt(&driverOpts)
			}
			captured.ReasoningEffort = driverOpts.ReasoningEffort
		}
		return driver, nil
	}
}

// toolBearingConversation is a history that carries tool_use/tool_result
// content blocks — the shape that made providers 400 when compaction sent it
// without a tools param.
func toolBearingConversation() []message.Message {
	return []message.Message{
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "read the file"},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "on it"},
			message.ToolCall{ID: "t1", Name: "read_file", Input: `{"path":"a.go"}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "t1", Name: "read_file", Content: "package main"},
		}},
	}
}

func TestCompactThreadUpdate_CarriesPersistedThreadMetadata(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	chatID := uuid.New().String()
	mainThreadID := chatID
	threadID := uuid.New().String()
	workflowID := uuid.New().String()
	threadTitle := "Fix generic pill title"
	originNodeID := "spawn-toolu_123"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "compact spawn", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: threadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		WorkflowID: &workflowID, Title: &threadTitle, Origin: db.ThreadOriginSpawn,
		OriginNodeID: &originNodeID, CreatedAt: now,
	})
	require.NoError(t, err)

	NewCompactActivity(repo, nil).emitThreadUpdate(ctx, chatID, threadID, "active", "Compact")

	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 100)
	require.NoError(t, err)
	require.Len(t, updates, 1)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(updates[0].Data), &payload))
	assert.Equal(t, threadID, payload["thread"])
	assert.Equal(t, workflowID, payload["workflow_id"])
	assert.Equal(t, threadTitle, payload["thread_title"])
	assert.Equal(t, db.ThreadOriginSpawn, payload["origin"])
	assert.Equal(t, originNodeID, payload["origin_node_id"])
}

func TestGenerateCompactionSummary_UsesStandardRequestEnvelope(t *testing.T) {
	tests := []struct {
		name string
		// model the injected resolver probes back
		model models.Model
		// the reasoning effort the shared path must produce for that model:
		// reconciled against capabilities, never "disabled"
		wantReasoningEffort string
	}{
		{
			name:                "non-reasoning model omits thinking entirely",
			model:               models.Model{ID: "mock-model", Name: "Mock Model"},
			wantReasoningEffort: "",
		},
		{
			name:                "reasoning model gets a valid reconciled level, not disabled",
			model:               models.Model{ID: "mock-reasoning-model", Name: "Mock Reasoning Model", CanReason: true},
			wantReasoningEffort: "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &capturingCompactionDriver{model: tt.model}
			captured := &capturedDriverOptions{}
			compactActivity := NewCompactActivity(nil, capturingCompactionResolver(driver, captured))

			chat := &db.Chat{ID: "chat-1", UserID: "user-1"}
			summary, err := compactActivity.generateCompactionSummary(context.Background(), chat, toolBearingConversation(), nil)
			require.NoError(t, err)
			assert.Equal(t, "Mock response", summary)

			// Thinking configuration comes from the shared path: reconciled
			// against model capabilities, never explicitly disabled. This is
			// what makes the anthropic clear_thinking 400 structurally
			// impossible.
			assert.NotEqual(t, "disabled", captured.ReasoningEffort)
			assert.Equal(t, tt.wantReasoningEffort, captured.ReasoningEffort)

			// The summarization call passes no tools, so the shared history
			// sanitization must have flattened every tool block to text. This
			// is what makes the LiteLLM "tool calling without tools= param"
			// 400 structurally impossible.
			assert.Empty(t, driver.gotTools)
			require.NotEmpty(t, driver.gotMessages)
			for _, m := range driver.gotMessages {
				assert.Empty(t, m.ToolCalls(), "no tool_use blocks may survive on a tool-less request")
				assert.Empty(t, m.ToolResults(), "no tool_result blocks may survive on a tool-less request")
			}

			// The compaction trigger message is appended as the final user turn.
			last := driver.gotMessages[len(driver.gotMessages)-1]
			assert.Equal(t, message.User, last.Role)
			assert.Contains(t, last.Content().Text, "handoff")

			// The summarization prompt is preserved.
			require.Len(t, driver.gotPrompts, 1)
			assert.Contains(t, driver.gotPrompts[0], "summarizing conversations")

			// Compaction keeps its low temperature via standard preferences.
			require.Len(t, captured.Preferences, 1)
			require.NotNil(t, captured.Preferences[0].Temperature)
			assert.InDelta(t, 0.3, *captured.Preferences[0].Temperature, 1e-9)
		})
	}
}
