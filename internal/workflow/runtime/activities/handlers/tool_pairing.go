// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// ============================================================================
// THE TOOL-PAIRING INVARIANT
// ============================================================================
//
// HARD REQUIREMENT: we never send the LLM a tool call without a matching result.
//
// Providers (Anthropic, and OpenAI-shaped APIs via their own encodings) reject a
// conversation where an assistant turn contains tool_use blocks that the next
// turn does not answer. A rejected request is not a recoverable error for the
// user: the conversation cannot move forward, every retry replays the same bad
// history, and the chat is wedged. The same is true in reverse for a tool_result
// that references a tool_use the model never made.
//
// This file is the single place that says what "valid" means. Everything else —
// the repair pass, the tests, the boundary assertion — is expressed in terms of
// ValidateToolPairing so there is exactly one definition to keep honest.

// ToolPairingViolation describes one way a message history breaks the invariant.
type ToolPairingViolation struct {
	// Kind is the machine-readable violation category.
	Kind ToolPairingViolationKind
	// MessageID is the message the violation was found on ("" for synthesized
	// messages that never had a DB row).
	MessageID string
	// MessageIndex is the position in the history slice, for pinpointing a
	// violation in a conversation where message IDs repeat or are empty.
	MessageIndex int
	// ToolCallID is the tool call at fault.
	ToolCallID string
	// ToolName is the tool at fault, when known.
	ToolName string
}

// ToolPairingViolationKind enumerates the ways the invariant can break.
type ToolPairingViolationKind string

const (
	// ViolationMissingResult is an assistant tool_use with no tool_result
	// anywhere after it. This is the deadlock case the requirement targets.
	ViolationMissingResult ToolPairingViolationKind = "missing_result"

	// ViolationOrphanedResult is a tool_result whose tool_use is not present in
	// any preceding assistant message — e.g. a fork inherited the results but
	// cut before the call, or compaction dropped the assistant turn.
	ViolationOrphanedResult ToolPairingViolationKind = "orphaned_result"

	// ViolationResultNotImmediate is a tool_result that exists but does not sit
	// in the message directly following its assistant message. Anthropic
	// requires adjacency, not just presence.
	ViolationResultNotImmediate ToolPairingViolationKind = "result_not_immediate"

	// ViolationDuplicateResult is more than one result for the same tool call.
	ViolationDuplicateResult ToolPairingViolationKind = "duplicate_result"
)

func (v ToolPairingViolation) String() string {
	return fmt.Sprintf("%s(msg=%s idx=%d tool_call_id=%s tool=%s)",
		v.Kind, v.MessageID, v.MessageIndex, v.ToolCallID, v.ToolName)
}

// ValidateToolPairing reports every way msgs violates the tool-pairing
// invariant. An empty result means the history is safe to send to a provider.
//
// The rules enforced, in provider terms:
//  1. Every tool_use in an assistant message has exactly one tool_result.
//  2. That result is in the message IMMEDIATELY following the assistant message.
//  3. No tool_result references a tool_use that isn't in a preceding assistant
//     message.
func ValidateToolPairing(msgs []message.Message) []ToolPairingViolation {
	var violations []ToolPairingViolation

	// Every tool call the conversation actually made, and where.
	callIndex := make(map[string]int)
	callName := make(map[string]string)
	for i, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls() {
			callIndex[tc.ID] = i
			callName[tc.ID] = tc.Name
		}
	}

	// Results, by call, with a count so duplicates are visible.
	resultIndex := make(map[string]int)
	resultCount := make(map[string]int)
	for i, msg := range msgs {
		if msg.Role != message.Tool {
			continue
		}
		for _, tr := range msg.ToolResults() {
			resultCount[tr.ToolCallID]++
			if _, seen := resultIndex[tr.ToolCallID]; !seen {
				resultIndex[tr.ToolCallID] = i
			}
			if _, isRealCall := callIndex[tr.ToolCallID]; !isRealCall {
				violations = append(violations, ToolPairingViolation{
					Kind:         ViolationOrphanedResult,
					MessageID:    msg.ID,
					MessageIndex: i,
					ToolCallID:   tr.ToolCallID,
					ToolName:     tr.Name,
				})
			}
		}
	}

	for i, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls() {
			resIdx, hasResult := resultIndex[tc.ID]
			if !hasResult {
				violations = append(violations, ToolPairingViolation{
					Kind:         ViolationMissingResult,
					MessageID:    msg.ID,
					MessageIndex: i,
					ToolCallID:   tc.ID,
					ToolName:     tc.Name,
				})
				continue
			}
			// Adjacency: the answering message must be the very next one.
			if resIdx != i+1 {
				violations = append(violations, ToolPairingViolation{
					Kind:         ViolationResultNotImmediate,
					MessageID:    msg.ID,
					MessageIndex: i,
					ToolCallID:   tc.ID,
					ToolName:     tc.Name,
				})
			}
			if resultCount[tc.ID] > 1 {
				violations = append(violations, ToolPairingViolation{
					Kind:         ViolationDuplicateResult,
					MessageID:    msg.ID,
					MessageIndex: i,
					ToolCallID:   tc.ID,
					ToolName:     tc.Name,
				})
			}
		}
	}

	return violations
}

// summarizeViolations renders violations for a log line or error message.
// Capped so a pathological history cannot produce an unbounded log entry.
func summarizeViolations(violations []ToolPairingViolation) string {
	const maxListed = 10
	parts := make([]string, 0, min(len(violations), maxListed))
	for i, v := range violations {
		if i == maxListed {
			parts = append(parts, fmt.Sprintf("... and %d more", len(violations)-maxListed))
			break
		}
		parts = append(parts, v.String())
	}
	return strings.Join(parts, ", ")
}
