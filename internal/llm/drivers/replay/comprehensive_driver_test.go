// Copyright (c) 2025 Reliant Labs
package replay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComprehensiveReplayDriver(t *testing.T) {
	// Create test replay data
	testData := &ComprehensiveReplayData{
		RootSessionID: "root-session",
		Title:         "Test Conversation",
		Sessions: map[string]*ReplaySession{
			"root-session": {
				ID:    "root-session",
				Title: "Test Conversation",
				Agent: "primary",
				Messages: []ComprehensiveMessage{
					{
						ID:        "msg-1",
						SessionID: "root-session",
						Role:      "user",
						Content:   json.RawMessage(`[{"type":"text","data":{"text":"Hello, how are you?"}},{"type":"finish","data":{"reason":"stop"}}]`),
						CreatedAt: time.Now().Add(-5 * time.Minute),
					},
					{
						ID:        "msg-2",
						SessionID: "root-session",
						Role:      "assistant",
						Content:   json.RawMessage(`[{"type":"text","data":{"text":"I'm doing well, thank you!"}},{"type":"finish","data":{"reason":"stop"}}]`),
						Agent:     "primary",
						Model:     "test-model",
						CreatedAt: time.Now().Add(-4 * time.Minute),
					},
				},
				ChildSessions: []string{"child-session-1"},
				CreatedAt:     time.Now().Add(-5 * time.Minute),
				UpdatedAt:     time.Now(),
				MessageCount:  2,
			},
			"child-session-1": {
				ID:              "child-session-1",
				ParentSessionID: "root-session",
				Title:           "Research Task",
				Agent:           "research",
				Messages: []ComprehensiveMessage{
					{
						ID:        "msg-3",
						SessionID: "child-session-1",
						Role:      "user",
						Content:   json.RawMessage(`[{"type":"text","data":{"text":"Can you research this topic?"}},{"type":"finish","data":{"reason":"stop"}}]`),
						CreatedAt: time.Now().Add(-3 * time.Minute),
					},
					{
						ID:        "msg-4",
						SessionID: "child-session-1",
						Role:      "assistant",
						Content:   json.RawMessage(`[{"type":"text","data":{"text":"I'll research that for you."}},{"type":"finish","data":{"reason":"stop"}}]`),
						Agent:     "research",
						Model:     "research-model",
						CreatedAt: time.Now().Add(-2 * time.Minute),
					},
				},
				CreatedAt:    time.Now().Add(-3 * time.Minute),
				UpdatedAt:    time.Now(),
				MessageCount: 2,
			},
		},
		MessageOrder: []string{"msg-1", "msg-2", "msg-3", "msg-4"},
		CreatedAt:    time.Now().Add(-5 * time.Minute),
		ExtractedAt:  time.Now(),
	}

	// Create temp file
	tmpDir := t.TempDir()
	replayFile := filepath.Join(tmpDir, "test_replay.json")

	data, err := json.MarshalIndent(testData, "", "  ")
	require.NoError(t, err)

	err = os.WriteFile(replayFile, data, 0644)
	require.NoError(t, err)

	t.Run("LoadReplayFile", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)
		assert.NotNil(t, driver)
		assert.Equal(t, "root-session", driver.data.RootSessionID)
		assert.Len(t, driver.data.Sessions, 2)
	})

	t.Run("SendMessages", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)

		ctx := context.Background()

		// First call should return first assistant message
		resp, err := driver.SendMessages(ctx, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "I'm doing well, thank you!", resp.Content)
		assert.Equal(t, message.FinishReasonEndTurn, resp.FinishReason)

		// Second call should return second assistant message (from child session)
		resp, err = driver.SendMessages(ctx, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "I'll research that for you.", resp.Content)
		assert.Equal(t, "research", driver.currentAgent)
	})

	t.Run("StreamResponse", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)

		ctx := context.Background()
		ch := driver.StreamResponse(ctx, nil, nil, nil)

		var events []llm.DriverEvent
		for event := range ch {
			events = append(events, event)
		}

		// Should have content start, deltas, stop, and complete events
		assert.Greater(t, len(events), 3)
		assert.Equal(t, llm.EventContentStart, events[0].Type)
		assert.Equal(t, llm.EventComplete, events[len(events)-1].Type)
	})

	t.Run("GetProgress", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)

		processed, total := driver.GetProgress()
		assert.Equal(t, 0, processed)
		assert.Equal(t, 4, total)

		// Process one message
		ctx := context.Background()
		_, err = driver.SendMessages(ctx, nil, nil, nil)
		require.NoError(t, err)

		processed, total = driver.GetProgress()
		assert.Equal(t, 2, processed) // Both user and assistant messages marked as processed
		assert.Equal(t, 4, total)
	})

	t.Run("Reset", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)

		ctx := context.Background()

		// Process a message
		_, err = driver.SendMessages(ctx, nil, nil, nil)
		require.NoError(t, err)

		processed, _ := driver.GetProgress()
		assert.Greater(t, processed, 0)

		// Reset
		driver.Reset()

		processed, _ = driver.GetProgress()
		assert.Equal(t, 0, processed)
		assert.Equal(t, "root-session", driver.state.CurrentSessionID)
	})

	t.Run("GetSessionTree", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)

		tree := driver.GetSessionTree()
		assert.Len(t, tree["root-session"], 1)
		assert.Equal(t, "child-session-1", tree["root-session"][0])
	})

	t.Run("ExtractMessageParts", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)

		tests := []struct {
			name              string
			content           json.RawMessage
			expectedText      string
			expectedToolCalls int
			expectedFinish    message.FinishReason
		}{
			{
				name:              "Simple string fallback",
				content:           json.RawMessage(`"Hello world"`),
				expectedText:      "Hello world",
				expectedToolCalls: 0,
				expectedFinish:    message.FinishReasonEndTurn,
			},
			{
				name:              "Proper parts format",
				content:           json.RawMessage(`[{"type":"text","data":{"text":"Hello from parts"}}]`),
				expectedText:      "Hello from parts",
				expectedToolCalls: 0,
				expectedFinish:    message.FinishReasonEndTurn,
			},
			{
				name:              "Multiple parts",
				content:           json.RawMessage(`[{"type":"text","data":{"text":"Part 1"}},{"type":"text","data":{"text":" Part 2"}}]`),
				expectedText:      "Part 1 Part 2",
				expectedToolCalls: 0,
				expectedFinish:    message.FinishReasonEndTurn,
			},
			{
				name:              "With tool call",
				content:           json.RawMessage(`[{"type":"text","data":{"text":"Let me check that."}},{"type":"tool_call","data":{"ID":"123","Name":"search","Input":"{}"}},{"type":"finish","data":{"reason":"tool_use"}}]`),
				expectedText:      "Let me check that.",
				expectedToolCalls: 1,
				expectedFinish:    message.FinishReasonToolUse,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				msg := &ComprehensiveMessage{
					Content: tt.content,
				}
				content, toolCalls, finishReason, err := driver.extractMessageParts(msg)
				require.NoError(t, err)
				assert.Equal(t, tt.expectedText, content)
				assert.Equal(t, tt.expectedToolCalls, len(toolCalls))
				assert.Equal(t, tt.expectedFinish, finishReason)
			})
		}
	})

	t.Run("NoMoreMessages", func(t *testing.T) {
		driver, err := NewComprehensiveReplayDriver(replayFile)
		require.NoError(t, err)

		ctx := context.Background()

		// Process all assistant messages
		_, err = driver.SendMessages(ctx, nil, nil, nil) // First assistant message
		require.NoError(t, err)

		_, err = driver.SendMessages(ctx, nil, nil, nil) // Second assistant message
		require.NoError(t, err)

		// Should error when no more messages
		_, err = driver.SendMessages(ctx, nil, nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no more assistant messages")
	})
}

func TestComprehensiveReplayDataHelpers(t *testing.T) {
	testData := &ComprehensiveReplayData{
		RootSessionID: "root",
		Sessions: map[string]*ReplaySession{
			"root": {
				ID: "root",
				Messages: []ComprehensiveMessage{
					{ID: "msg-1", SessionID: "root", Role: "user"},
					{ID: "msg-2", SessionID: "root", Role: "assistant"},
				},
			},
			"child": {
				ID:              "child",
				ParentSessionID: "root",
				Messages: []ComprehensiveMessage{
					{ID: "msg-3", SessionID: "child", Role: "user"},
					{ID: "msg-4", SessionID: "child", Role: "assistant"},
				},
			},
		},
		MessageOrder: []string{"msg-1", "msg-2", "msg-3", "msg-4"},
	}

	t.Run("GetSessionTree", func(t *testing.T) {
		tree := testData.GetSessionTree()
		assert.Len(t, tree["root"], 1)
		assert.Equal(t, "child", tree["root"][0])
	})

	t.Run("GetMessagesForSession", func(t *testing.T) {
		messages := testData.GetMessagesForSession("root")
		assert.Len(t, messages, 2)
		assert.Equal(t, "msg-1", messages[0].ID)

		messages = testData.GetMessagesForSession("nonexistent")
		assert.Nil(t, messages)
	})

	t.Run("GetAllMessagesInOrder", func(t *testing.T) {
		messages := testData.GetAllMessagesInOrder()
		assert.Len(t, messages, 4)
		assert.Equal(t, "msg-1", messages[0].ID)
		assert.Equal(t, "msg-4", messages[3].ID)
	})

	t.Run("FindNextMessage", func(t *testing.T) {
		state := &ReplayState{
			CurrentSessionID:  "root",
			SessionMessageIdx: make(map[string]int),
			ProcessedMessages: make(map[string]bool),
		}

		msg, session, err := testData.FindNextMessage(state)
		require.NoError(t, err)
		assert.Equal(t, "msg-1", msg.ID)
		assert.Equal(t, "root", session.ID)

		// Mark as processed and find next
		msg, _, err = testData.FindNextMessage(state)
		require.NoError(t, err)
		assert.Equal(t, "msg-2", msg.ID)
	})
}
