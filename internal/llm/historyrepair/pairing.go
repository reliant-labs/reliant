// Copyright (c) 2025 Reliant Labs
package historyrepair

import (
	"github.com/reliant-labs/reliant/internal/models/message"
)

// pairToolResults makes every assistant turn's tool calls answered, each
// exactly once, by the tool message(s) directly after it — the adjacency
// Anthropic and OpenAI Chat Completions require, and the pairing every other
// provider needs.
//
// A turn whose following tool messages already answer exactly its calls is
// kept as it is, message for message. Otherwise its results are rebuilt as
// one tool message, in call order: each call takes its result from the tool
// messages after it when it has one there, else from anywhere in the history
// (a misplaced result), else a stub. A tool message that follows no assistant
// turn is dropped, its results available to the turn they answer. Whatever
// result is left over answers no call, or answers one a second time, and is
// dropped.
//
// It runs after dedupeCallIDs, so tool-call ids are unique.
func pairToolResults(ms []message.Message, r *Report) []message.Message {
	type ref struct{ msg, part int }
	results := make(map[string][]ref) // tool-call id -> results in Tool messages, in order
	total := 0
	for i, m := range ms {
		if m.Role != message.Tool {
			continue
		}
		for j, part := range m.Parts {
			if tr, ok := part.(message.ToolResult); ok {
				results[tr.ToolCallID] = append(results[tr.ToolCallID], ref{i, j})
				total++
			}
		}
	}

	out := make([]message.Message, 0, len(ms))
	placed := 0
	answered := make(map[string]bool)
	for i := 0; i < len(ms); i++ {
		m := ms[i]
		if m.Role == message.Tool {
			// Follows no assistant turn: its results are placed by lookup.
			if !hasContent(m) {
				r.DroppedMessages++
			}
			continue
		}
		out = append(out, m)
		calls := m.ToolCalls()
		if m.Role != message.Assistant || len(calls) == 0 {
			continue
		}
		end := i + 1
		for end < len(ms) && ms[end].Role == message.Tool {
			end++
		}
		run := ms[i+1 : end]
		if answersExactly(run, calls) {
			out = append(out, run...)
			for _, tc := range calls {
				answered[tc.ID] = true
			}
			placed += len(calls)
			i = end - 1
			continue
		}

		parts := make([]message.ContentPart, 0, len(calls))
		for _, tc := range calls {
			var found *ref
			for k, at := range results[tc.ID] {
				if at.msg > i && at.msg < end {
					found = &results[tc.ID][k]
					break
				}
			}
			if found == nil && len(results[tc.ID]) > 0 && !answered[tc.ID] {
				found = &results[tc.ID][0]
				r.MovedResults++
			}
			if found != nil && !answered[tc.ID] {
				parts = append(parts, ms[found.msg].Parts[found.part])
				placed++
			} else {
				parts = append(parts, message.ToolResult{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    MissingToolResultContent,
					IsError:    true,
				})
				r.StubbedResults++
			}
			answered[tc.ID] = true
		}
		tool := message.Message{
			ID:              m.ID + "-tool-results",
			Role:            message.Tool,
			SessionID:       m.SessionID,
			Thread:          m.Thread,
			ContextSequence: m.ContextSequence,
		}
		if len(run) > 0 {
			tool = run[0]
		}
		tool.Parts = parts
		out = append(out, tool)
		i = end - 1
	}

	// Every result not placed was dropped: it answered no call in the
	// history, or answered one that already had its result.
	orphaned := 0
	for id, refs := range results {
		if !answered[id] {
			orphaned += len(refs)
		}
	}
	r.OrphanedResults += orphaned
	r.DuplicateResults += total - placed - orphaned
	return out
}

// answersExactly reports whether the tool messages in run hold one result for
// each call and nothing else.
func answersExactly(run []message.Message, calls []message.ToolCall) bool {
	want := make(map[string]bool, len(calls))
	for _, tc := range calls {
		want[tc.ID] = true
	}
	n := 0
	for _, m := range run {
		for _, part := range m.Parts {
			tr, ok := part.(message.ToolResult)
			if !ok {
				continue
			}
			if !want[tr.ToolCallID] {
				return false // foreign, or a second result for one call
			}
			delete(want, tr.ToolCallID)
			n++
		}
	}
	return n == len(calls) && len(want) == 0
}

// hasContent reports whether a driver would send anything for m.
func hasContent(m message.Message) bool {
	for _, part := range m.Parts {
		switch p := part.(type) {
		case message.TextContent:
			if p.Text != "" {
				return true
			}
		case message.BinaryContent, message.ImageURLContent:
			if m.Role != message.Tool {
				return true
			}
		case message.ReasoningContent:
			if m.Role == message.Assistant && (p.Thinking != "" || p.Signature != "") {
				return true
			}
		case message.RedactedReasoningContent:
			if m.Role == message.Assistant && p.Data != "" {
				return true
			}
		case message.ToolCall:
			if m.Role == message.Assistant {
				return true
			}
		case message.ToolResult:
			if m.Role == message.Tool {
				return true
			}
		}
	}
	return false
}

// dropEmptyMessages drops a message no driver would send anything for. A user
// message gets placeholder text instead: dropping it could put two assistant
// turns side by side, or leave the history ending on the assistant.
func dropEmptyMessages(ms []message.Message, r *Report) []message.Message {
	out := ms[:0]
	for _, m := range ms {
		if hasContent(m) {
			out = append(out, m)
			continue
		}
		if m.Role == message.User {
			m.Parts = append(m.Parts, message.TextContent{Text: PlaceholderText})
			r.PlaceholderMessages++
			out = append(out, m)
			continue
		}
		r.DroppedMessages++
	}
	return out
}

// openWithUser puts a placeholder user turn before the first non-system
// message when that is an assistant turn. Anthropic and Gemini refuse a
// conversation that opens with the model, and a second compaction can leave
// its summary first (hermes #69512). A System message before it does not
// count: Codex sends one as a developer message, not a user turn.
func openWithUser(ms []message.Message, r *Report) []message.Message {
	i := 0
	for i < len(ms) && ms[i].Role == message.System {
		i++
	}
	if i == len(ms) || ms[i].Role != message.Assistant {
		return ms
	}
	r.OpeningPlaceholder++
	opening := message.Message{
		Role:            message.User,
		SessionID:       ms[i].SessionID,
		Thread:          ms[i].Thread,
		ContextSequence: ms[i].ContextSequence,
		Parts:           []message.ContentPart{message.TextContent{Text: PlaceholderText}},
	}
	out := make([]message.Message, 0, len(ms)+1)
	out = append(out, ms[:i]...)
	out = append(out, opening)
	return append(out, ms[i:]...)
}

// mergeUserTurns merges each user message into the user message before it.
// Every part stays its own block, never concatenated into one string: the
// merged turn then sends the same blocks a later request would, and the
// provider's prompt cache keeps matching (hermes #112358).
func mergeUserTurns(ms []message.Message, r *Report) []message.Message {
	out := ms[:0]
	for _, m := range ms {
		if n := len(out); n > 0 && m.Role == message.User && out[n-1].Role == message.User {
			out[n-1].Parts = append(out[n-1].Parts, m.Parts...)
			r.MergedUserTurns++
			continue
		}
		out = append(out, m)
	}
	return out
}
