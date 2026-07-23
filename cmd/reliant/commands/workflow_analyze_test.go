// Copyright (c) 2025 Reliant Labs
package commands

import (
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

func strptr(s string) *string { return &s }

func TestSummarizeAskUser(t *testing.T) {
	meta := `{"type":"ask_user","questions":[{"question":"Continue?","options":[{"label":"Continue"},{"label":"Feedback"}]}]}`
	got := summarizeAskUser(strptr(meta))
	if !strings.Contains(got, "Continue?") || !strings.Contains(got, "[Continue / Feedback]") {
		t.Fatalf("summarizeAskUser = %q, want prompt + option labels", got)
	}

	if summarizeAskUser(nil) != "" {
		t.Fatalf("summarizeAskUser(nil) should be empty")
	}

	// Non-ask_user metadata falls back to a truncated raw string.
	if got := summarizeAskUser(strptr("not json")); got != "not json" {
		t.Fatalf("fallback = %q, want raw", got)
	}
}

func TestSummarizeAnswer(t *testing.T) {
	// selected + freetext shape (the real forge-one-shot answer form).
	rd := `{"answers":[{"question":"q","selected":["Orders + Rx"],"freetext":"keep scope minimal"}]}`
	got := summarizeAnswer(rd)
	if !strings.Contains(got, "Orders + Rx") || !strings.Contains(got, "keep scope minimal") {
		t.Fatalf("summarizeAnswer = %q, want selected + freetext", got)
	}

	// answer/reply shape.
	if got := summarizeAnswer(`{"answers":[{"answer":"yes"}]}`); got != "yes" {
		t.Fatalf("summarizeAnswer(answer) = %q, want yes", got)
	}

	// Unparseable falls back to raw.
	if got := summarizeAnswer("plain text"); got != "plain text" {
		t.Fatalf("summarizeAnswer(raw) = %q", got)
	}
}

func TestLooksLikeStructuredResponse(t *testing.T) {
	verdict := map[string]interface{}{"grade": "fail", "strategy": "continue", "feedback": "x"}
	if !looksLikeStructuredResponse("builtin://structured-agent", verdict) {
		t.Fatal("structured-agent verdict should be recognized")
	}
	// Recognized by shape even without an agent hint.
	if !looksLikeStructuredResponse("", verdict) {
		t.Fatal("grade+strategy shape should be recognized")
	}
	// An ordinary tool call from a normal agent is not a structured response.
	ordinary := map[string]interface{}{"path": "/tmp/x", "content": "y"}
	if looksLikeStructuredResponse("builtin://agent", ordinary) {
		t.Fatal("ordinary tool call should not be a structured response")
	}
}

func TestExtractStructuredOutputs(t *testing.T) {
	m := &db.Message{
		ID:       "m1",
		ThreadID: "t1",
		Agent:    strptr("builtin://structured-agent"),
	}
	blocks := []*db.MessageContentBlock{
		{
			MessageID: "m1",
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolName:  strptr("submit_evaluation"),
			ToolInput: strptr(`{"grade":"pass","strategy":"pass","feedback":"lgtm"}`),
		},
		{
			MessageID: "m1",
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolName:  strptr("submit_evaluation"),
			Content:   strptr("lgtm"),
		},
	}
	out := extractStructuredOutputs(m, blocks)
	if len(out) != 1 {
		t.Fatalf("got %d structured outputs, want 1", len(out))
	}
	if out[0].Tool != "submit_evaluation" || out[0].Data["grade"] != "pass" {
		t.Fatalf("unexpected structured output: %+v", out[0])
	}
}

func TestWfaDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{500 * time.Millisecond, "500ms"},
		{90 * time.Second, "1m30s"},
		{2*time.Hour + 3*time.Minute + 4*time.Second, "2h3m4s"},
	}
	for _, c := range cases {
		if got := wfaDuration(c.d); got != c.want {
			t.Errorf("wfaDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestWfaThousands(t *testing.T) {
	if got := wfaThousands(172124); got != "172,124" {
		t.Errorf("wfaThousands = %q, want 172,124", got)
	}
	if got := wfaThousands(42); got != "42" {
		t.Errorf("wfaThousands = %q, want 42", got)
	}
}
