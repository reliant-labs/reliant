// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"fmt"

	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// ErrStreamEndedWithoutTerminalEvent is what a Responses driver reports when
// its SSE stream ends with no transport error but before any terminal event —
// `response.completed`, `response.incomplete` or `response.failed` — arrived.
//
// Without the terminal event there is no response object: no status, no usage,
// no authoritative list of output items. The stream that was read is a prefix
// of a response, not a response, so the turn is an error and is retried rather
// than reported as an end of turn. It matches drivererrors.ErrStreamEndedEarly under
// errors.Is, which is what classifies it as transient.
var ErrStreamEndedWithoutTerminalEvent = fmt.Errorf(
	"responses stream closed before a terminal event (response.completed): %w", drivererrors.ErrStreamEndedEarly)

// FunctionCallKey is the key both Responses drivers index a streamed function
// call under: the output item's id, which is what argument deltas reference,
// else its call_id.
func FunctionCallKey(fc responses.ResponseFunctionToolCall) string {
	if fc.ID != "" {
		return fc.ID
	}
	return fc.CallID
}

// ToolCallGate decides which function calls of a terminal response may be
// executed.
//
// A `completed` response's calls all may. An `incomplete`, `failed` or
// `cancelled` response is a different matter: it can carry a function call
// whose arguments were cut off mid-JSON, or one the model was still writing
// when the provider gave up. The agent loop runs whatever tool calls a turn
// returns (stopreason.Derive checks for tool calls before it looks at the
// finish reason), so returning those would execute a half-written request. So:
//
//   - failed / cancelled: no call runs. The provider has disowned the response.
//   - incomplete: a call runs only when the response's own output lists its
//     function_call item with item status `completed` — the provider's word
//     that the item, arguments included, was finished before the response
//     stopped (for example "interrupted", which Codex CLI treats as a
//     completed turn that wants a follow-up). Everything else is dropped.
//
// Any other status, including one a mock left empty, is read as completed,
// matching FinishReason.
type ToolCallGate struct {
	all       bool
	confirmed map[string]struct{}
}

// GateToolCalls builds the gate for a terminal response. A nil response gates
// everything out: there is nothing to confirm a call against.
func GateToolCalls(resp *responses.Response) ToolCallGate {
	if resp == nil {
		return ToolCallGate{}
	}
	switch resp.Status {
	case responses.ResponseStatusFailed, responses.ResponseStatusCancelled:
		return ToolCallGate{}
	case responses.ResponseStatusIncomplete:
		gate := ToolCallGate{confirmed: map[string]struct{}{}}
		for _, out := range resp.Output {
			fc := out.AsFunctionCall()
			if fc.Type != "function_call" || fc.CallID == "" {
				continue
			}
			if fc.Status == responses.ResponseFunctionToolCallStatusCompleted {
				gate.confirmed[FunctionCallKey(fc)] = struct{}{}
			}
		}
		return gate
	default:
		return ToolCallGate{all: true}
	}
}

// Allows reports whether the call indexed under key (see FunctionCallKey) may
// be executed.
func (g ToolCallGate) Allows(key string) bool {
	if g.all {
		return true
	}
	_, ok := g.confirmed[key]
	return ok
}
