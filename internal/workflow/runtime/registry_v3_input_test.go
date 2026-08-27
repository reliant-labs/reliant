// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Every v3 activity is dispatched as types.ActivityInput{Runtime, Node}, and
// the identifiers live on Runtime — one struct deeper than extractChatID and
// extractThread used to look. So writeErrorEvent answered "no chat_id in
// input" for CallLLM and SaveMessage alike and wrote nothing, while
// web/src/components/Chat/WorkflowErrorMessage.tsx sat fully built, retry
// states and all, and never rendered once.
//
// Measured over a day of worker logs: 26 error events attempted, 26 skipped, 0
// written. That is how chat 58cc003f could fail five SaveMessage attempts in a
// row and still look, from the UI, like a chat that simply stopped.

func v3Input(chatID, thread string) types.ActivityInput {
	return types.ActivityInput{
		Runtime: types.RuntimeContext{ChatID: chatID, Thread: thread},
	}
}

func TestExtractChatID_ReadsV3ActivityInput(t *testing.T) {
	if got := extractChatID(v3Input("chat-58cc003f", "thread-58cc003f")); got != "chat-58cc003f" {
		t.Errorf("extractChatID() = %q, want %q — without it no activity error reaches the chat", got, "chat-58cc003f")
	}
}

func TestExtractThread_ReadsV3ActivityInput(t *testing.T) {
	if got := extractThread(v3Input("chat-58cc003f", "thread-58cc003f")); got != "thread-58cc003f" {
		t.Errorf("extractThread() = %q, want %q", got, "thread-58cc003f")
	}
}

// The end-to-end assertion, in the shape the helper tests cannot make: a
// failing v3 activity must actually write the error event.
func TestWriteErrorEventReachesChatForV3Input(t *testing.T) {
	repo := &capturingRepo{}
	wrapper := NewActivityWrapper(
		"SaveMessage",
		func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
			return nil, nil
		},
		repo,
	)

	wrapper.writeErrorEvent(
		context.Background(),
		v3Input("chat-58cc003f", "thread-58cc003f"),
		"SaveMessage",
		"82",
		1,
		"workflow-58cc003f",
		errors.New("refusing to save an assistant message with no content, tool calls, or thinking"),
		5,
	)

	if len(repo.updates) != 1 {
		t.Fatalf("expected 1 chat update, got %d — a v3 activity failure is invisible to the user", len(repo.updates))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(repo.updates[0]), &payload); err != nil {
		t.Fatalf("error payload is not valid JSON: %v", err)
	}
	if payload["chat_id"] != "chat-58cc003f" {
		t.Errorf("chat_id = %v, want chat-58cc003f", payload["chat_id"])
	}
	if payload["thread"] != "thread-58cc003f" {
		t.Errorf("thread = %v, want thread-58cc003f", payload["thread"])
	}
}

// The same one-level miss emptied the per-step audit trail: writeStepExecution
// skips a row it cannot key by workflow_id + step_id, so step_executions has
// ZERO rows for chats the v3 runtime ran. A chat that stopped then has no
// durable record of the step it stopped on.
func TestExtractActivityInputInfo_ReadsV3ActivityInput(t *testing.T) {
	info := extractActivityInputInfo(types.ActivityInput{
		Runtime: types.RuntimeContext{
			ChatID:        "chat-58cc003f",
			Thread:        "thread-58cc003f",
			WorkflowID:    "workflow-58cc003f",
			StepID:        "call_llm",
			LoopNodeID:    "agent_loop",
			LoopIteration: 1,
		},
	})

	if info.ChatID != "chat-58cc003f" {
		t.Errorf("ChatID = %q, want chat-58cc003f", info.ChatID)
	}
	if info.WorkflowID != "workflow-58cc003f" {
		t.Errorf("WorkflowID = %q, want workflow-58cc003f", info.WorkflowID)
	}
	if info.StepID != "call_llm" {
		t.Errorf("StepID = %q, want call_llm — an unkeyed step execution is never written", info.StepID)
	}
	if info.LoopNodeID != "agent_loop" {
		t.Errorf("LoopNodeID = %q, want agent_loop", info.LoopNodeID)
	}
	if info.LoopIteration != 1 {
		t.Errorf("LoopIteration = %d, want 1", info.LoopIteration)
	}
}

// A field the input carries itself always wins over one found in a nested
// struct — the descent adds answers, it must never replace them.
func TestExtractInputString_OwnFieldWinsOverNested(t *testing.T) {
	type nested struct {
		ChatID string `json:"chat_id"`
	}
	input := struct {
		Nested nested `json:"nested"`
		ChatID string `json:"chat_id"`
	}{
		Nested: nested{ChatID: "chat-nested"},
		ChatID: "chat-own",
	}

	if got := extractChatID(input); got != "chat-own" {
		t.Errorf("extractChatID() = %q, want chat-own", got)
	}
}
