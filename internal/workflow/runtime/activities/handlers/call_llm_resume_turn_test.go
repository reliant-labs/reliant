// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
)

// A loop resumed mid-work with nothing new to read continues its cut-off turn
// (RuntimeContext.ResumeTurn), instead of yielding it as the agent's answer.
// reliant #2394: sub-agent ae9723c5 stopped on a usage limit with a
// thinking-only turn at its tail; the capacity wake resumed it, its first turn
// yielded on that tail, and it "failed again" without doing any work.

// historyRecordingDriver streams one fixed turn and records the history each
// call was sent.
type historyRecordingDriver struct {
	mu    sync.Mutex
	sent  [][]message.Message
	reply string
}

func (d *historyRecordingDriver) Name() string { return "mock" }
func (d *historyRecordingDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}
func (d *historyRecordingDriver) ValidateKey(context.Context) error { return nil }
func (d *historyRecordingDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: d.reply, FinishReason: message.FinishReasonEndTurn}, nil
}

func (d *historyRecordingDriver) StreamResponse(_ context.Context, _ []string, msgs []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	d.mu.Lock()
	d.sent = append(d.sent, append([]message.Message(nil), msgs...))
	d.mu.Unlock()
	ch := make(chan llm.DriverEvent, 3)
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: d.reply}
	ch <- llm.DriverEvent{
		Type:     llm.EventComplete,
		Response: &llm.DriverResponse{Content: d.reply, FinishReason: message.FinishReasonEndTurn, Usage: llm.TokenUsage{TokenCount: 30}},
	}
	close(ch)
	return ch
}

func (d *historyRecordingDriver) calls() [][]message.Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]message.Message(nil), d.sent...)
}

func withResumeTurn(input ActivityInput) ActivityInput {
	input.Runtime.ResumeTurn = true
	input.Runtime.WorkflowID = input.Runtime.ChatID
	input.Runtime.MessageIdempotencyKey = "call_llm|test|resume"
	return input
}

// hiddenContinueRows returns the persisted hidden continue messages on thread.
func hiddenContinueRows(t *testing.T, h *IdempotencyTestHelper, chatID string) []*db.Message {
	t.Helper()
	msgs, err := h.Repo().ListMessages(context.Background(), chatID, db.MessageListOptions{})
	require.NoError(t, err)
	var rows []*db.Message
	for _, m := range msgs {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_USER {
			continue
		}
		blocks, err := h.Repo().ListContentBlocks(context.Background(), m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil && *b.Content == resumeContinueText {
				rows = append(rows, m)
				break
			}
		}
	}
	return rows
}

func TestCallLLM_ResumeTurnContinuesACutOffTurn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		cutOffs func(t *testing.T, h *IdempotencyTestHelper, chatID string)
	}{
		{"partial reply", func(t *testing.T, h *IdempotencyTestHelper, chatID string) {
			saveTextOnlyAssistantMessage(t, h, chatID, chatID, "I'll run the migration and then")
		}},
		{"reasoning only", func(t *testing.T, h *IdempotencyTestHelper, chatID string) {
			_, err := threads.NewService(h.Repo()).SaveMessage(context.Background(), threads.SaveMessageOpts{
				ChatID:   chatID,
				Thread:   chatID,
				Role:     int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
				Thinking: &threads.ThinkingContent{Content: "Weighing tmpfs against a bind mount...", Signature: "sig"},
			})
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := &historyRecordingDriver{reply: "Continuing: the migration ran."}

			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
			chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
			h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
			tc.cutOffs(t, h, chat.ID)

			resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			})
			activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, resolver, nil)
			input := withResumeTurn(callLLMInput(chat.ID, chat.ID, "mock-model"))

			var output CallLLMOutput
			require.NoError(t, h.ExecuteActivity(activityInstance.Execute, input, &output))

			calls := driver.calls()
			require.Len(t, calls, 1, "a resumed cut-off turn must call the provider, not yield")
			sent := calls[0]
			last := sent[len(sent)-1]
			assert.Equal(t, message.User, last.Role, "the request ends with the continue message")
			assert.Equal(t, resumeContinueText, last.Content().String())
			assert.Equal(t, "Continuing: the migration ran.", output.ResponseText)

			// Saved hidden, so the visible transcript is unchanged and the
			// conversation stays well-formed for later turns.
			rows := hiddenContinueRows(t, h, chat.ID)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].DisplayStyle)
			assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *rows[0].DisplayStyle)

			// A retry of the same activity finds its own row: one continue
			// message per resumed turn, however often it runs.
			driver2 := &historyRecordingDriver{reply: "again"}
			retry := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, drivers.DriverResolver(
				func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
					return driver2, nil
				}), nil)
			_ = h.ExecuteActivity(retry.Execute, input, &output)
			assert.Len(t, hiddenContinueRows(t, h, chat.ID), 1, "a retry must not add a second continue message")
		})
	}
}

// Only a resumed turn continues: an ordinary turn on the same tail still
// yields, and a resume that brought new input (history ends with the user)
// needs no continue message.
func TestCallLLM_ResumeTurnLeavesOtherTurnsAlone(t *testing.T) {
	t.Parallel()

	t.Run("not a resume", func(t *testing.T) {
		t.Parallel()
		driver := &historyRecordingDriver{reply: "never"}
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()
		ctx := context.Background()
		project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
		chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
		h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
		saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "All done.")

		activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, drivers.DriverResolver(
			func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			}), nil)
		var output CallLLMOutput
		require.NoError(t, h.ExecuteActivity(activityInstance.Execute, callLLMInput(chat.ID, chat.ID, "mock-model"), &output))
		assert.Empty(t, driver.calls(), "an ordinary turn on an assistant tail still yields")
		assert.Empty(t, hiddenContinueRows(t, h, chat.ID))
	})

	t.Run("resume with new input", func(t *testing.T) {
		t.Parallel()
		driver := &historyRecordingDriver{reply: "answered"}
		h := NewIdempotencyTestHelper(t)
		defer h.Cleanup()
		ctx := context.Background()
		project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
		chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
		saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "I'll run the migration and then")
		h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

		activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, drivers.DriverResolver(
			func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			}), nil)
		var output CallLLMOutput
		require.NoError(t, h.ExecuteActivity(activityInstance.Execute, withResumeTurn(callLLMInput(chat.ID, chat.ID, "mock-model")), &output))
		require.Len(t, driver.calls(), 1)
		assert.Empty(t, hiddenContinueRows(t, h, chat.ID), "the new input is the turn's input; no continue message")
	})
}
