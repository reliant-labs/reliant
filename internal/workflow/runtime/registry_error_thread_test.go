// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// capturingRepo records the chat_updates an activity error writes, so the test
// asserts the PAYLOAD the frontend actually receives rather than just the
// helper that feeds it.
type capturingRepo struct {
	db.Repository
	updates []string
}

func (r *capturingRepo) CreateChatUpdate(_ context.Context, _ string, _ db.UpdateType, _ string, data string) error {
	r.updates = append(r.updates, data)
	return nil
}

// An activity error must name the thread it happened on.
//
// InterleavedTimeline scopes an error that carries a thread to that thread and
// shows a thread-less one EVERYWHERE (it cannot guess, and guessing is what
// produced a wrong-thread render before). Nothing was filling the field in, so
// every activity error was chat-global in practice: a run of
// DrainAgentMessages failures rendered at the top of a spawn thread that did
// not exist when those failures happened.
func TestExtractThread(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: "",
		},
		{
			name:     "map with thread",
			input:    map[string]interface{}{"chat_id": "chat-1", "thread": "thread-abc"},
			expected: "thread-abc",
		},
		{
			name:     "map without thread",
			input:    map[string]interface{}{"chat_id": "chat-1"},
			expected: "",
		},
		{
			name: "struct with json tag thread",
			input: struct {
				ChatID string `json:"chat_id"`
				Thread string `json:"thread"`
			}{ChatID: "chat-2", Thread: "thread-json"},
			expected: "thread-json",
		},
		{
			name: "struct with Thread field name and no tag",
			input: struct {
				ChatID string
				Thread string
			}{ChatID: "chat-3", Thread: "thread-byname"},
			expected: "thread-byname",
		},
		{
			name: "pointer to struct",
			input: &struct {
				ChatID string `json:"chat_id"`
				Thread string `json:"thread"`
			}{ChatID: "chat-4", Thread: "thread-ptr"},
			expected: "thread-ptr",
		},
		{
			name: "omitempty tag still matches",
			input: struct {
				Thread string `json:"thread,omitempty"`
			}{Thread: "thread-omitempty"},
			expected: "thread-omitempty",
		},
		{
			name: "chat-scoped activity reports no thread",
			input: struct {
				ChatID string `json:"chat_id"`
			}{ChatID: "chat-5"},
			expected: "",
		},
		{
			// The real input behind the reported bug.
			name: "DrainAgentMessagesInput carries its recipient thread",
			input: types.DrainAgentMessagesInput{
				ChatID: "chat-6",
				Thread: "thread-recipient",
			},
			expected: "thread-recipient",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractThread(tt.input); got != tt.expected {
				t.Errorf("extractThread() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// The end-to-end assertion: the error event a failing activity writes must
// carry the thread, because that is the field InterleavedTimeline filters on.
// The helper test above can pass while the wiring is missing — which is
// exactly the state that produced the reported bug.
func TestWriteErrorEventCarriesThread(t *testing.T) {
	t.Parallel()
	repo := &capturingRepo{}
	wrapper := NewActivityWrapper(
		"DrainAgentMessages",
		func(_ context.Context, _ types.DrainAgentMessagesInput) (types.DrainAgentMessagesOutput, error) {
			return types.DrainAgentMessagesOutput{}, nil
		},
		repo,
	)

	wrapper.writeErrorEvent(
		context.Background(),
		types.DrainAgentMessagesInput{ChatID: "chat-1", Thread: "thread-recipient"},
		"DrainAgentMessages",
		"activity-1",
		1,
		"workflow-1",
		errors.New("drain failed"),
		3,
	)

	if len(repo.updates) != 1 {
		t.Fatalf("expected 1 chat update, got %d", len(repo.updates))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(repo.updates[0]), &payload); err != nil {
		t.Fatalf("error payload is not valid JSON: %v", err)
	}

	thread, ok := payload["thread"].(string)
	if !ok {
		t.Fatalf("error event has no thread field; the timeline will render it "+
			"in EVERY thread of the chat, including spawns that started after it. payload=%v", payload)
	}
	if thread != "thread-recipient" {
		t.Errorf("thread = %q, want %q", thread, "thread-recipient")
	}
}

// A chat-scoped activity must OMIT the field rather than send "". The timeline
// branches on presence, so an empty string would be a thread that matches no
// thread — the error would vanish instead of showing everywhere.
func TestWriteErrorEventOmitsAbsentThread(t *testing.T) {
	t.Parallel()
	repo := &capturingRepo{}
	wrapper := NewActivityWrapper(
		"ChatScoped",
		func(_ context.Context, _ struct {
			ChatID string `json:"chat_id"`
		}) (struct{}, error) {
			return struct{}{}, nil
		},
		repo,
	)

	wrapper.writeErrorEvent(
		context.Background(),
		struct {
			ChatID string `json:"chat_id"`
		}{ChatID: "chat-1"},
		"ChatScoped",
		"activity-2",
		1,
		"workflow-1",
		errors.New("boom"),
		3,
	)

	if len(repo.updates) != 1 {
		t.Fatalf("expected 1 chat update, got %d", len(repo.updates))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(repo.updates[0]), &payload); err != nil {
		t.Fatalf("error payload is not valid JSON: %v", err)
	}
	if _, present := payload["thread"]; present {
		t.Errorf("chat-scoped error must omit thread entirely, got %v", payload["thread"])
	}
}

// A nil typed pointer must not panic — reflection on a nil pointer's Elem is
// invalid, and an activity error is exactly the moment we cannot afford a
// second failure while reporting the first.
func TestExtractThreadNilPointer(t *testing.T) {
	t.Parallel()
	var input *types.DrainAgentMessagesInput
	if got := extractThread(input); got != "" {
		t.Errorf("extractThread(nil pointer) = %q, want empty", got)
	}
}
