// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// A tool result lives in TWO places: the durable `tool_call_results` row that
// ExecuteTools writes, and a tool_result content block on a TOOL-role message
// that SaveMessage writes. History is assembled ONLY from content blocks.
//
// When the row commits and the message does not, the result is durably recorded
// and completely invisible to the model — and repairMessageHistory then
// synthesizes InterruptedToolResultContent, telling the model the outcome is
// unknown while the real answer sits in the database.
//
// Observed on chat 128cf4f5: a completed spawn_status call whose result row was
// written at 20:30:32.786 ("Agent a6825dec… is STILL RUNNING after 5.6s") but
// whose TOOL message never was. The parent then had no idea its spawn was alive.

// toolResultRepo is a db.Repository that only implements GetToolCallResult.
// Embedding the interface means every other method panics if called, which is
// the point: this test asserts recovery consults exactly one thing.
type toolResultRepo struct {
	db.Repository
	results map[string]*db.ToolCallResult
	calls   int
}

func (r *toolResultRepo) GetToolCallResult(_ context.Context, toolCallID string) (*db.ToolCallResult, error) {
	r.calls++
	return r.results[toolCallID], nil
}

func assistantWithToolCall(id, callID, name string) message.Message {
	return message.Message{
		ID:   id,
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: callID, Name: name},
		},
	}
}

func TestRecoverPersistedToolResults(t *testing.T) {
	ctx := context.Background()

	t.Run("recovers a durable result missing from history", func(t *testing.T) {
		repo := &toolResultRepo{results: map[string]*db.ToolCallResult{
			"tc1": {ToolCallID: "tc1", Content: "Agent is STILL RUNNING after 5.6s"},
		}}
		msgs := []message.Message{
			userWithText("u1", "check on it"),
			assistantWithToolCall("a1", "tc1", "spawn_status"),
		}

		out := recoverPersistedToolResults(ctx, repo, msgs)

		if len(out) != 3 {
			t.Fatalf("expected a TOOL message to be appended, got %d messages", len(out))
		}
		last := out[len(out)-1]
		if last.Role != message.Tool {
			t.Fatalf("recovered message must have TOOL role, got %v", last.Role)
		}
		results := last.ToolResults()
		if len(results) != 1 {
			t.Fatalf("expected 1 recovered result, got %d", len(results))
		}
		// The REAL content, not a placeholder.
		if results[0].Content != "Agent is STILL RUNNING after 5.6s" {
			t.Fatalf("expected the durable result content, got %q", results[0].Content)
		}
		if results[0].IsError {
			t.Fatal("a successful durable result must not be marked as an error")
		}
	})

	// The recovered result must sit IMMEDIATELY after its assistant message, or
	// the tool-pairing invariant fails and CallLLM refuses to build a request.
	t.Run("places the result directly after its assistant message", func(t *testing.T) {
		repo := &toolResultRepo{results: map[string]*db.ToolCallResult{
			"tc1": {ToolCallID: "tc1", Content: "done"},
		}}
		msgs := []message.Message{
			assistantWithToolCall("a1", "tc1", "bash"),
			userWithText("u2", "later message"),
		}

		out := recoverPersistedToolResults(ctx, repo, msgs)

		if len(out) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(out))
		}
		if out[0].ID != "a1" || out[1].Role != message.Tool || out[2].ID != "u2" {
			t.Fatalf("recovered result is misplaced: got roles %v, %v, %v",
				out[0].Role, out[1].Role, out[2].Role)
		}
	})

	// A call that ALREADY has a result in history must not be looked up or
	// duplicated — duplicates break tool pairing just as badly as omissions.
	t.Run("leaves already-answered tool calls alone", func(t *testing.T) {
		repo := &toolResultRepo{results: map[string]*db.ToolCallResult{
			"tc1": {ToolCallID: "tc1", Content: "durable"},
		}}
		msgs := []message.Message{
			assistantWithToolCall("a1", "tc1", "bash"),
			{ID: "t1", Role: message.Tool, Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "tc1", Content: "already here"},
			}},
		}

		out := recoverPersistedToolResults(ctx, repo, msgs)

		if len(out) != 2 {
			t.Fatalf("nothing should be appended, got %d messages", len(out))
		}
		if repo.calls != 0 {
			t.Fatalf("an answered tool call must not be looked up, got %d lookups", repo.calls)
		}
	})

	// No durable row: leave it for repairMessageHistory, whose "outcome
	// unknown" placeholder is the correct answer for a tool that never
	// reported. Recovery must not invent anything.
	t.Run("does not fabricate a result when none is persisted", func(t *testing.T) {
		repo := &toolResultRepo{results: map[string]*db.ToolCallResult{}}
		msgs := []message.Message{assistantWithToolCall("a1", "tc1", "bash")}

		out := recoverPersistedToolResults(ctx, repo, msgs)

		if len(out) != 1 {
			t.Fatalf("nothing should be appended when no durable row exists, got %d", len(out))
		}
	})

	// An empty durable result is not a result — treating "" as recovered would
	// hand the model a blank tool answer instead of the honest placeholder.
	t.Run("ignores an empty durable result", func(t *testing.T) {
		repo := &toolResultRepo{results: map[string]*db.ToolCallResult{
			"tc1": {ToolCallID: "tc1", Content: ""},
		}}
		msgs := []message.Message{assistantWithToolCall("a1", "tc1", "bash")}

		out := recoverPersistedToolResults(ctx, repo, msgs)

		if len(out) != 1 {
			t.Fatalf("an empty durable result must not be recovered, got %d messages", len(out))
		}
	})

	t.Run("preserves the error flag of a durable failure", func(t *testing.T) {
		repo := &toolResultRepo{results: map[string]*db.ToolCallResult{
			"tc1": {ToolCallID: "tc1", Content: "command failed", IsError: true},
		}}
		msgs := []message.Message{assistantWithToolCall("a1", "tc1", "bash")}

		out := recoverPersistedToolResults(ctx, repo, msgs)

		results := out[len(out)-1].ToolResults()
		if len(results) != 1 || !results[0].IsError {
			t.Fatal("a durable failure must stay marked as an error")
		}
	})

	t.Run("nil repo and empty history are safe", func(t *testing.T) {
		if out := recoverPersistedToolResults(ctx, nil, []message.Message{
			assistantWithToolCall("a1", "tc1", "bash"),
		}); len(out) != 1 {
			t.Fatal("a nil repo must be a no-op")
		}
		if out := recoverPersistedToolResults(ctx, &toolResultRepo{}, nil); len(out) != 0 {
			t.Fatal("empty history must be a no-op")
		}
	})
}

// End-to-end shape of the production failure: recovery must leave history in a
// state CallLLM accepts, i.e. not ending on an assistant message with an
// unanswered tool call.
func TestRecoverPersistedToolResults_UnwedgesHistoryTail(t *testing.T) {
	repo := &toolResultRepo{results: map[string]*db.ToolCallResult{
		"toolu_013mq": {ToolCallID: "toolu_013mq", Content: "Agent is STILL RUNNING after 5.6s"},
	}}
	msgs := []message.Message{
		userWithText("u1", "check the spawn"),
		assistantWithToolCall("a1", "toolu_013mq", "spawn_status"),
	}

	out := recoverPersistedToolResults(context.Background(), repo, msgs)

	if last := out[len(out)-1]; last.Role == message.Assistant {
		t.Fatal("history still ends on an assistant message with an unanswered tool call — " +
			"CallLLM's guard would still refuse to build a request")
	}
}
