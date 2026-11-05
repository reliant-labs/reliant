// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0
package follow

import (
	"strings"
	"testing"
)

func TestIsStuckStep(t *testing.T) {
	cases := map[string]bool{
		"stuck_checkpoint":  true,
		"STUCK_CHECKPOINT":  true,
		"review_checkpoint": false,
		"stuck":             true,
		"scaffold_stuck":    true,
		"plan":              false,
		"":                  false,
	}
	for step, want := range cases {
		if got := IsStuckStep(step); got != want {
			t.Errorf("IsStuckStep(%q) = %v, want %v", step, got, want)
		}
	}
}

func TestRenderQuestionStuckVsRoutine(t *testing.T) {
	stuck := Event{
		Event:       EventQuestion,
		ExecutionID: "chat-1",
		Question:    questionInfo("q1", "stuck_checkpoint", "thread-a", `{"type":"ask_user","questions":[{"question":"Help?","options":[{"label":"Fixed it"}]}]}`),
	}
	routine := Event{
		Event:       EventQuestion,
		ExecutionID: "chat-1",
		Question:    questionInfo("q2", "review_checkpoint", "thread-b", `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`),
	}

	if !stuck.Question.Stuck {
		t.Fatal("stuck_checkpoint question should be flagged Stuck")
	}
	if routine.Question.Stuck {
		t.Fatal("review_checkpoint question must not be flagged Stuck")
	}
	if s := RenderText(stuck); !strings.Contains(s, "STUCK") {
		t.Errorf("stuck gate render = %q, want a STUCK alert", s)
	}
	if s := RenderText(routine); strings.Contains(s, "STUCK") {
		t.Errorf("routine gate render = %q, must not claim STUCK", s)
	}
}

// TestRenderQuestionNamesThread pins that a gate line says WHICH thread is
// waiting. A fanned-out run has many threads open at once and only some are
// gated, so "a question was raised" is not actionable on its own.
func TestRenderQuestionNamesThread(t *testing.T) {
	ev := Event{
		Event:       EventQuestion,
		ExecutionID: "chat-1",
		Question:    questionInfo("q1", "review_checkpoint", "1ea189a5-0000-0000-0000-000000000000", `{"type":"ask_user","questions":[{"question":"Proceed?"}]}`),
	}
	line := RenderText(ev)
	if !strings.Contains(line, "thread 1ea189a5") {
		t.Errorf("question line does not name its thread:\n%s", line)
	}

	// A gate with no thread id must not render an empty "thread" fragment.
	bare := RenderText(Event{Event: EventQuestion, ExecutionID: "chat-1", Question: questionInfo("q2", "review_checkpoint", "", "")})
	if strings.Contains(bare, "thread ") {
		t.Errorf("expected no thread fragment when the id is unknown:\n%s", bare)
	}
}
