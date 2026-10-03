// Copyright (c) 2025 Reliant Labs
package stopreason

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

func TestDerive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		turn Turn
		want string
		why  string
	}{
		{"clean finish", Turn{FinishReason: message.FinishReasonEndTurn, ProducedText: true}, Done,
			"the model chose to stop"},
		{"tool calls", Turn{FinishReason: message.FinishReasonToolUse, ToolCalls: 2}, ToolUse,
			"tools were requested"},
		{"tool_use with no surviving calls", Turn{FinishReason: message.FinishReasonToolUse}, Done,
			"nothing to run means nothing pending"},
		{"interrupt beats tool calls", Turn{FinishReason: message.FinishReasonToolUse, ToolCalls: 1, Interrupted: true}, Interrupted,
			"a cut stream's calls are whatever had arrived"},
		{"interrupt beats a clean finish reason", Turn{FinishReason: message.FinishReasonEndTurn, Interrupted: true}, Interrupted,
			"the provider never finished this turn"},
		{"tool calls beat truncation", Turn{FinishReason: message.FinishReasonMaxTokens, ToolCalls: 1}, ToolUse,
			"the calls run and their results are new input"},
		{"tool calls beat a pause", Turn{FinishReason: message.FinishReasonPauseTurn, ToolCalls: 1, ProducedText: true}, ToolUse,
			"the calls run regardless"},
		{"truncated", Turn{FinishReason: message.FinishReasonMaxTokens}, Truncated,
			"out of output room"},
		{"refused", Turn{FinishReason: message.FinishReasonRefusal}, Refused,
			"the safety system declined"},
		{"pause with text", Turn{FinishReason: message.FinishReasonPauseTurn, ProducedText: true}, Incomplete,
			"the model has more to do and handed back progress"},
		{"pause with nothing", Turn{FinishReason: message.FinishReasonPauseTurn}, Error,
			"continuing would re-send a byte-identical request"},
		{"cancelled", Turn{FinishReason: message.FinishReasonCancelled}, Error, "provider-side cancel"},
		{"provider error", Turn{FinishReason: message.FinishReasonError}, Error, "transport or provider failure"},
		{"tool dispatch error", Turn{FinishReason: message.FinishReasonToolUseError}, Error, "failed before completion"},
		{"permission denied", Turn{FinishReason: message.FinishReasonPermissionDenied}, Error, "blocked before completion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Derive(tc.turn); got != tc.want {
				t.Fatalf("Derive(%+v) = %q, want %q — %s", tc.turn, got, tc.want, tc.why)
			}
		})
	}
}

// An unrecognized reason must never read as a clean finish: that is what makes
// a new provider behavior look like success.
func TestDeriveUnknownIsNeverDone(t *testing.T) {
	t.Parallel()
	for _, reason := range []message.FinishReason{
		message.FinishReasonUnknown, "", "some_reason_a_provider_adds_next_year",
	} {
		if got := Derive(Turn{FinishReason: reason, ProducedText: true}); got != Error {
			t.Fatalf("Derive(%q) = %q, want %q — an unrecognized stop must not be "+
				"treated as done or as a reason to continue", reason, got, Error)
		}
	}
}

// The vocabulary is closed: every finish reason lands on one of seven values.
// A loop written against them must never meet an eighth.
func TestDeriveVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		Interrupted: true, ToolUse: true, Refused: true, Truncated: true,
		Incomplete: true, Error: true, Done: true,
	}
	reasons := []message.FinishReason{
		message.FinishReasonEndTurn, message.FinishReasonMaxTokens, message.FinishReasonToolUse,
		message.FinishReasonToolUseError, message.FinishReasonCancelled, message.FinishReasonError,
		message.FinishReasonPermissionDenied, message.FinishReasonRefusal, message.FinishReasonPauseTurn,
		message.FinishReasonUnknown, "", "novel",
	}
	for _, reason := range reasons {
		for _, calls := range []int{0, 1} {
			for _, interrupted := range []bool{false, true} {
				for _, text := range []bool{false, true} {
					turn := Turn{FinishReason: reason, ToolCalls: calls, Interrupted: interrupted, ProducedText: text}
					if got := Derive(turn); !allowed[got] {
						t.Fatalf("Derive(%+v) = %q, outside the closed vocabulary", turn, got)
					}
				}
			}
		}
	}
}

func TestFillDefault(t *testing.T) {
	t.Parallel()

	t.Run("absent with tool calls becomes tool_use", func(t *testing.T) {
		out := map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"name": "bash"}}}
		FillDefault(out)
		if out["stop_reason"] != ToolUse {
			t.Fatalf("stop_reason = %v, want %q", out["stop_reason"], ToolUse)
		}
	})

	t.Run("absent without tool calls becomes done", func(t *testing.T) {
		for _, out := range []map[string]interface{}{
			{},
			{"tool_calls": []interface{}{}},
			{"tool_calls": nil},
			{"stop_reason": ""},
			{"stop_reason": nil},
		} {
			FillDefault(out)
			if out["stop_reason"] != Done {
				t.Fatalf("stop_reason = %v, want %q for %v", out["stop_reason"], Done, out)
			}
		}
	})

	t.Run("a set value is left alone", func(t *testing.T) {
		out := map[string]interface{}{"stop_reason": Truncated, "tool_calls": []interface{}{"x"}}
		FillDefault(out)
		if out["stop_reason"] != Truncated {
			t.Fatalf("stop_reason = %v, a set value must not be overwritten", out["stop_reason"])
		}
	})

	t.Run("nil map does not panic", func(t *testing.T) {
		FillDefault(nil)
	})
}
