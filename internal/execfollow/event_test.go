// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"strings"
	"testing"
)

func TestParseHookFlag(t *testing.T) {
	cases := []struct {
		raw     string
		want    Hook
		wantErr bool
	}{
		{raw: "on=node_failed cmd=echo hi", want: Hook{On: "node_failed", Cmd: "echo hi"}},
		{raw: "on=any cmd=notify.sh --level=warn 'a b'", want: Hook{On: "any", Cmd: "notify.sh --level=warn 'a b'"}},
		{raw: "on=workflow_completed   cmd=cat > /tmp/x", want: Hook{On: "workflow_completed", Cmd: "cat > /tmp/x"}},
		{raw: "  on=node_started cmd=true  ", want: Hook{On: "node_started", Cmd: "true"}},
		// Command containing "cmd=" again keeps everything after the first.
		{raw: "on=node_completed cmd=env cmd=x", want: Hook{On: "node_completed", Cmd: "env cmd=x"}},
		{raw: "node_failed cmd=echo", wantErr: true},         // missing on=
		{raw: "on=node_failed", wantErr: true},               // missing cmd=
		{raw: "on=node_failed echo hi", wantErr: true},       // missing cmd= prefix
		{raw: "on=bogus_event cmd=echo hi", wantErr: true},   // unknown event
		{raw: "on=workflow_started cmd=echo", wantErr: true}, // not hookable (only via any)
		{raw: "on=node_failed cmd=", wantErr: true},          // empty command
		{raw: "", wantErr: true},
	}

	for _, tc := range cases {
		got, err := ParseHookFlag(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseHookFlag(%q) expected error, got %+v", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHookFlag(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseHookFlag(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestHookMatches(t *testing.T) {
	cases := []struct {
		hook  Hook
		event string
		want  bool
	}{
		{Hook{On: "node_failed"}, EventNodeFailed, true},
		{Hook{On: "node_failed"}, EventNodeCompleted, false},
		{Hook{On: "workflow_completed"}, EventWorkflowCompleted, true},
		{Hook{On: "workflow_failed"}, EventWorkflowFailed, true},
		{Hook{On: "workflow_failed"}, EventWorkflowCancelled, false},
		{Hook{On: "any"}, EventNodeStarted, true},
		{Hook{On: "any"}, EventWorkflowCancelled, true},
		{Hook{On: "any"}, EventFollowTimeout, true},
	}
	for _, tc := range cases {
		if got := tc.hook.Matches(tc.event); got != tc.want {
			t.Errorf("Hook{On:%q}.Matches(%q) = %v, want %v", tc.hook.On, tc.event, got, tc.want)
		}
	}
}

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
