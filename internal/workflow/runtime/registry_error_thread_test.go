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
// files a thread-less one under the MAIN thread, the same default messages
// use. So an activity that runs on a spawn but reports no thread has its
// failure shown in the wrong place: a run of DrainAgentMessages failures
// rendered at the top of a spawn thread that did not exist when those failures
// happened, and later EnqueueAgentMessage failures did the same.
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
		{
			name: "input that declares its thread is not field-guessed",
			input: declaredThreadInput{
				ChatID:       "chat-7",
				FromThreadID: "thread-spawn",
				ToThreadID:   "thread-parent",
			},
			expected: "thread-spawn",
		},
		{
			name: "declared thread through a pointer",
			input: &declaredThreadInput{
				ChatID:       "chat-8",
				FromThreadID: "thread-spawn",
				ToThreadID:   "thread-parent",
			},
			expected: "thread-spawn",
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

// declaredThreadInput stands in for handlers.EnqueueAgentMessageInput, which
// this package cannot import (handlers imports runtime). Same shape: two
// threads, neither named "thread", and only the activity knows it ran on the
// sending one. The handlers side asserts the real input against
// ThreadScopedInput, so the two cannot drift apart.
type declaredThreadInput struct {
	ChatID       string `json:"chat_id"`
	FromThreadID string `json:"from_thread_id"`
	ToThreadID   string `json:"to_thread_id"`
}

func (in declaredThreadInput) ActivityThread() string { return in.FromThreadID }

// The reported bug, end to end. A background spawn reports its outcome to its
// parent through EnqueueAgentMessage, from the spawn's own goroutine. Its
// input names from_thread_id/to_thread_id and no "thread", so its failures were
// written thread-less — and six of them rendered at the top of an unrelated
// spawn thread that did not exist yet when they happened.
func TestWriteErrorEventCarriesDeclaredThread(t *testing.T) {
	t.Parallel()
	repo := &capturingRepo{}
	wrapper := NewActivityWrapper(
		"EnqueueAgentMessage",
		func(_ context.Context, _ declaredThreadInput) (struct{}, error) {
			return struct{}{}, nil
		},
		repo,
	)

	wrapper.writeErrorEvent(
		context.Background(),
		declaredThreadInput{ChatID: "chat-1", FromThreadID: "thread-spawn", ToThreadID: "chat-1"},
		"EnqueueAgentMessage",
		"activity-3",
		1,
		"workflow-1",
		errors.New("failed to enqueue agent message"),
		3,
	)

	if len(repo.updates) != 1 {
		t.Fatalf("expected 1 chat update, got %d", len(repo.updates))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(repo.updates[0]), &payload); err != nil {
		t.Fatalf("error payload is not valid JSON: %v", err)
	}
	if got := payload["thread"]; got != "thread-spawn" {
		t.Errorf("thread = %v, want %q (the spawn whose goroutine ran the activity)", got, "thread-spawn")
	}
}

// A chat-scoped activity must OMIT the field rather than send "". Absent means
// "chat-level work", which the timeline files under the main thread; a
// sentinel "" would make every consumer re-learn that the two are the same.
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
