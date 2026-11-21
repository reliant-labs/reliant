// Copyright (c) 2025 Reliant Labs

// Package historyrepair makes a message history safe to send to a provider.
//
// It is the one repair step every driver's request goes through, run by the
// driver wrapper (drivers.baseDriver) right before the driver converts the
// history into its wire format. A provider refuses a malformed history on
// every later turn, so a single bad row — a tool result whose call was
// compacted away, a crash between persisting a call and its result, a blank
// text block, a tool-call id another provider minted — would otherwise wedge
// the chat for good. Before this package, only the Anthropic driver repaired
// anything at request time (it dropped orphaned tool results), and every
// other driver sent the history as it found it.
//
// The repair works on a copy: the stored history is never changed, and the
// same history always produces the same request, so a provider's prompt cache
// keeps matching from one turn to the next. A healthy history passes through
// unchanged, and Apply is idempotent.
//
// The rules, in the order they run:
//
//  1. An Agent message (harness-written) is sent as a User message. No driver
//     converts the Agent role; each one silently dropped it.
//  2. Blank text parts are dropped (Anthropic: "text content blocks must
//     contain non-whitespace text"), and tool-call input that is not a JSON
//     object is replaced with {} (hermes #58168: corrupt arguments 400 on
//     every replay).
//  3. A tool-call id that repeats an earlier call's id gets a _dup<n> suffix,
//     on the call and on the results that answer it (Anthropic and the
//     Responses API refuse duplicate ids; some servers name every call
//     call_0).
//  4. Every assistant turn's tool calls are answered by the tool message(s)
//     right after it: a misplaced result is moved there, a result answering
//     no call (or a duplicate result) is dropped, and a call with no result
//     gets a stub error result.
//  5. Messages left with nothing a driver would send are dropped; a user
//     message left empty gets placeholder text instead, so dropping it cannot
//     join the turns on either side.
//  6. A history that opens with an assistant turn gets a placeholder user turn
//     before it (hermes #69512).
//  7. Per provider (Rules.MergeConsecutiveUserTurns): consecutive user
//     messages are merged into one, each part kept as its own block.
//  8. Per provider (Rules.ToolCallIDs): a tool-call id the provider would
//     refuse is replaced with one derived from a hash of it, on the call and
//     its results alike.
//
// A missing result is STUBBED, never resolved by dropping the call: removing
// a tool call from an assistant turn would change a turn that may carry a
// signed thinking block, and Anthropic verifies that signature against the
// turn as it was produced. Inserting a result after the turn leaves the turn
// byte-identical. This also covers the trailing turn: no request is built
// while a call is genuinely in flight (tools run between model calls), so a
// history ending in an unanswered call is one whose run died mid-tool
// (hermes #49201), and every provider refuses an unanswered call wherever it
// sits.
//
// Consecutive assistant turns are never merged, and a tool message is never
// merged with a user message. Every driver converts one message at a time and
// reads only the first text part and the first reasoning part of an assistant
// message, and ignores text on a tool message and tool results on a user
// message, so either merge would silently lose content. The providers that
// receive consecutive same-role turns (Anthropic, OpenAI) accept them.
package historyrepair

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Rules are the ways providers genuinely differ in what history they accept.
// The zero value applies only the repairs every provider needs.
type Rules struct {
	// ToolCallIDs constrains the tool-call ids the provider accepts. The zero
	// value replays every id verbatim, which a driver must keep when its ids
	// carry data (the LiteLLM gateway embeds a Gemini thought signature in
	// the id).
	ToolCallIDs IDRules

	// MergeConsecutiveUserTurns merges adjacent user messages into one, for a
	// provider that refuses two user turns in a row (Gemini: "multiturn
	// requests must alternate", hermes #55125). Set it only for a driver that
	// sends every text and binary part of a user message: one that reads only
	// the first text part would drop the rest.
	MergeConsecutiveUserTurns bool
}

// IDRules describe the tool-call ids a provider accepts. An id that breaks
// any of them is replaced (see rewriteID).
type IDRules struct {
	// Charset restricts ids to [a-zA-Z0-9_-]: Anthropic's ^[a-zA-Z0-9_-]+$.
	Charset bool
	// MaxLen is the longest id accepted; 0 means no limit. OpenAI Chat
	// Completions takes at most 40 characters, the Responses API 64.
	MaxLen int
}

// AnthropicIDRules are the ids the Anthropic Messages API accepts.
func AnthropicIDRules() IDRules { return IDRules{Charset: true} }

// PlaceholderText stands in for a user turn that has no content of its own.
// Never a lone space, which Anthropic rejects as blank.
const PlaceholderText = "(empty)"

// MissingToolResultContent is the result given to a tool call that has none.
const MissingToolResultContent = "Tool call was not completed: no result was recorded for it. Verify its effects before re-running it."

// Report counts what Apply repaired. All zero means the history was sent as
// it was (apart from the Agent role, which is routine).
type Report struct {
	AgentMessages       int // Agent messages sent as User
	BlankTextParts      int // blank text parts dropped
	InvalidToolInputs   int // tool-call inputs replaced with {}
	DuplicateCallIDs    int // tool calls renamed for repeating an earlier id
	OrphanedResults     int // results answering no call, dropped
	DuplicateResults    int // second and later results for one call, dropped
	MovedResults        int // results moved next to their call
	StubbedResults      int // calls given a stub result
	DroppedMessages     int // messages with nothing left to send, dropped
	PlaceholderMessages int // empty user messages given placeholder text
	OpeningPlaceholder  int // placeholder user turns put before an opening assistant turn
	MergedUserTurns     int // user messages merged into the one before them
	RewrittenIDs        int // tool-call ids replaced to meet IDRules
}

// ForRequest is Apply with logging: what a driver wrapper calls. A repair that
// means the history was damaged logs a warning with the counts; one that only
// adapts a healthy history to the provider logs at debug.
func ForRequest(ctx context.Context, msgs []message.Message, rules Rules) []message.Message {
	out, r := Apply(msgs, rules)
	damaged := r.BlankTextParts + r.InvalidToolInputs + r.DuplicateCallIDs + r.OrphanedResults +
		r.DuplicateResults + r.MovedResults + r.StubbedResults + r.DroppedMessages +
		r.PlaceholderMessages + r.OpeningPlaceholder
	adapted := r.MergedUserTurns + r.RewrittenIDs
	switch {
	case damaged > 0:
		logging.WarnContext(ctx, "[historyrepair] Repaired message history before the provider request", reportArgs(r, len(msgs), len(out))...)
	case adapted > 0:
		logging.DebugContext(ctx, "[historyrepair] Adapted message history to the provider", reportArgs(r, len(msgs), len(out))...)
	}
	return out
}

func reportArgs(r Report, before, after int) []any {
	return []any{
		"messages_before", before,
		"messages_after", after,
		"blank_text_parts", r.BlankTextParts,
		"invalid_tool_inputs", r.InvalidToolInputs,
		"duplicate_call_ids", r.DuplicateCallIDs,
		"orphaned_results", r.OrphanedResults,
		"duplicate_results", r.DuplicateResults,
		"moved_results", r.MovedResults,
		"stubbed_results", r.StubbedResults,
		"dropped_messages", r.DroppedMessages,
		"placeholder_messages", r.PlaceholderMessages,
		"opening_placeholder", r.OpeningPlaceholder,
		"merged_user_turns", r.MergedUserTurns,
		"rewritten_ids", r.RewrittenIDs,
	}
}

// Apply returns msgs repaired by the package's rules, and what it changed.
// msgs is not modified.
func Apply(msgs []message.Message, rules Rules) ([]message.Message, Report) {
	var r Report
	ms := cloneMessages(msgs)
	ms = normalizeRoles(ms, &r)
	cleanParts(ms, &r)
	dedupeCallIDs(ms, &r)
	ms = pairToolResults(ms, &r)
	ms = dropEmptyMessages(ms, &r)
	ms = openWithUser(ms, &r)
	if rules.MergeConsecutiveUserTurns {
		ms = mergeUserTurns(ms, &r)
	}
	rewriteIDs(ms, rules.ToolCallIDs, &r)
	return ms, r
}

// cloneMessages copies every message and its parts slice, so no rule can
// write through to the caller's history.
func cloneMessages(msgs []message.Message) []message.Message {
	out := make([]message.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].Parts = append([]message.ContentPart(nil), m.Parts...)
	}
	return out
}

func normalizeRoles(ms []message.Message, r *Report) []message.Message {
	for i := range ms {
		if ms[i].Role == message.Agent {
			ms[i].Role = message.User
			r.AgentMessages++
		}
	}
	return ms
}

// cleanParts drops blank text parts and replaces tool-call input that is not a
// JSON object with {}.
func cleanParts(ms []message.Message, r *Report) {
	for i := range ms {
		parts := ms[i].Parts[:0]
		for _, part := range ms[i].Parts {
			switch p := part.(type) {
			case message.TextContent:
				if strings.TrimSpace(p.Text) == "" {
					r.BlankTextParts++
					continue
				}
			case message.ToolCall:
				if ms[i].Role == message.Assistant && !isJSONObject(p.Input) {
					p.Input = "{}"
					part = p
					r.InvalidToolInputs++
				}
			}
			parts = append(parts, part)
		}
		ms[i].Parts = parts
	}
}

func isJSONObject(s string) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal([]byte(s), &obj) == nil && obj != nil
}

// liteLLMThoughtSignatureSeparator is where the LiteLLM gateway's tool-call
// ids carry a Gemini thought signature (llm.NewToolCallIDKeepingThoughtSignature).
// A _dup suffix goes before it so the signature stays the id's tail.
const liteLLMThoughtSignatureSeparator = "__thought__"

// dedupeCallIDs renames a tool call whose id repeats an earlier call's, and
// points the tool results that answer it at the new id.
//
// Which call a result answers is decided by position: a result answers the
// most recent assistant turn holding its id, taking that turn's calls with
// the id in order. So in a history where two turns both named their call
// call_0, the result after the second turn is the second call's.
func dedupeCallIDs(ms []message.Message, r *Report) {
	taken := make(map[string]bool)
	for _, m := range ms {
		if m.Role == message.Assistant {
			for _, tc := range m.ToolCalls() {
				taken[tc.ID] = true
			}
		}
	}
	seen := make(map[string]bool)
	pending := make(map[string][]string) // original id -> ids not yet answered, oldest first
	last := make(map[string]string)      // original id -> the id its latest result was given
	for i := range ms {
		switch ms[i].Role {
		case message.Assistant:
			fresh := make(map[string]bool)
			for j, part := range ms[i].Parts {
				tc, ok := part.(message.ToolCall)
				if !ok {
					continue
				}
				orig := tc.ID
				if !fresh[orig] {
					// A newer turn holding the id takes over the results
					// that follow it.
					fresh[orig] = true
					pending[orig] = nil
				}
				if seen[orig] {
					tc.ID = uniqueID(orig, taken)
					taken[tc.ID] = true
					ms[i].Parts[j] = tc
					r.DuplicateCallIDs++
				}
				seen[orig] = true
				pending[orig] = append(pending[orig], tc.ID)
			}
		case message.Tool:
			for j, part := range ms[i].Parts {
				tr, ok := part.(message.ToolResult)
				if !ok {
					continue
				}
				id := tr.ToolCallID
				if q := pending[id]; len(q) > 0 {
					id = q[0]
					pending[tr.ToolCallID] = q[1:]
					last[tr.ToolCallID] = id
				} else if prev, ok := last[tr.ToolCallID]; ok {
					id = prev
				}
				if id != tr.ToolCallID {
					tr.ToolCallID = id
					ms[i].Parts[j] = tr
				}
			}
		}
	}
}
