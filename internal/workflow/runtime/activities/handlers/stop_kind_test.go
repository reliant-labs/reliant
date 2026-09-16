// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// stop_kind exists because zero tool calls is ambiguous: a model that finished
// and a model that was cut off mid-thought produce identical output, and every
// while-condition in this repo tests tool_calls. See CallLLMOutput.stop_kind.

func TestDeriveStopKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		reason message.FinishReason
		want   string
		why    string
	}{
		{message.FinishReasonEndTurn, StopKindComplete, "the model chose to stop"},
		{message.FinishReasonToolUse, StopKindComplete, "stopping to call a tool is a normal complete turn"},

		{message.FinishReasonMaxTokens, StopKindTruncated,
			"ran out of output room — this is the case that made chat 2308c394 " +
				"burn 868s and 64000 output tokens and still read as a clean finish"},

		{message.FinishReasonRefusal, StopKindRefused, "the safety system declined"},

		{message.FinishReasonCancelled, StopKindCancelled, "stream cut"},
		{message.FinishReasonError, StopKindCancelled, "transport or provider error"},
		{message.FinishReasonToolUseError, StopKindCancelled, "tool dispatch failed"},
		{message.FinishReasonPermissionDenied, StopKindCancelled, "blocked before completion"},
		{message.FinishReasonPauseTurn, StopKindCancelled,
			"suspended mid-turn expecting resumption; nothing resumes it here, so " +
				"the turn is a fragment and must not read as complete"},
	}

	for _, tc := range cases {
		t.Run(string(tc.reason), func(t *testing.T) {
			if got := deriveStopKind(tc.reason); got != tc.want {
				t.Fatalf("deriveStopKind(%q) = %q, want %q — %s", tc.reason, got, tc.want, tc.why)
			}
		})
	}
}

// An unrecognized stop reason must NOT read as complete.
//
// This is the whole point of the field. Treating an unknown provider stop as a
// clean finish is exactly how a new provider behavior silently becomes "the
// agent finished successfully" — the failure mode stop_kind exists to end. A
// provider that adds a stop reason tomorrow should degrade to "something cut
// this short", which is loud and recoverable, not to "done".
func TestDeriveStopKindUnknownIsNotComplete(t *testing.T) {
	t.Parallel()

	for _, reason := range []message.FinishReason{
		message.FinishReasonUnknown,
		message.FinishReason("some_reason_a_provider_adds_next_year"),
		message.FinishReason(""),
	} {
		if got := deriveStopKind(reason); got == StopKindComplete {
			t.Fatalf("deriveStopKind(%q) = %q — an unrecognized stop reason must never "+
				"read as complete, or a new provider behavior silently reports as success",
				reason, got)
		}
	}
}

// The vocabulary is closed. A loop author writing `stop_kind == 'complete'`
// depends on there being no fifth value to forget about.
func TestStopKindVocabularyIsClosed(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		StopKindComplete:  true,
		StopKindTruncated: true,
		StopKindRefused:   true,
		StopKindCancelled: true,
	}

	everyKnownReason := []message.FinishReason{
		message.FinishReasonEndTurn, message.FinishReasonMaxTokens,
		message.FinishReasonToolUse, message.FinishReasonToolUseError,
		message.FinishReasonCancelled, message.FinishReasonError,
		message.FinishReasonPermissionDenied, message.FinishReasonRefusal,
		message.FinishReasonPauseTurn, message.FinishReasonUnknown,
		message.FinishReason("unrecognized"),
	}

	for _, reason := range everyKnownReason {
		if got := deriveStopKind(reason); !allowed[got] {
			t.Fatalf("deriveStopKind(%q) produced %q, which is outside the closed "+
				"four-value vocabulary; workflow conditions match on these positively",
				reason, got)
		}
	}
}
