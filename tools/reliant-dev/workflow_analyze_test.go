// Copyright (c) 2025 Reliant Labs
package main

import (
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// strPtr is defined in workflow_false_green_test.go.

func TestSummarizeAskUser(t *testing.T) {
	meta := `{"type":"ask_user","questions":[{"question":"Continue?","options":[{"label":"Continue"},{"label":"Feedback"}]}]}`
	got := summarizeAskUser(strPtr(meta))
	if !strings.Contains(got, "Continue?") || !strings.Contains(got, "[Continue / Feedback]") {
		t.Fatalf("summarizeAskUser = %q, want prompt + option labels", got)
	}

	if summarizeAskUser(nil) != "" {
		t.Fatalf("summarizeAskUser(nil) should be empty")
	}

	// Non-ask_user metadata falls back to a truncated raw string.
	if got := summarizeAskUser(strPtr("not json")); got != "not json" {
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
		Agent:    strPtr("builtin://structured-agent"),
	}
	blocks := []*db.MessageContentBlock{
		{
			MessageID: "m1",
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolName:  strPtr("submit_evaluation"),
			ToolInput: strPtr(`{"grade":"pass","strategy":"pass","feedback":"lgtm"}`),
		},
		{
			MessageID: "m1",
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolName:  strPtr("submit_evaluation"),
			Content:   strPtr("lgtm"),
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

// A fan-out unit that spent 87% of its life asleep in a provider retry ladder is
// indistinguishable, on wall clock alone, from one that was simply slow. The
// retry ladder runs inside one Temporal activity attempt, so it leaves no
// message, no step execution and no status change — measured on run b7aa4056,
// where reconstructing "113s of a 129s life" took a manual forensic pass.
func TestWfaProviderWait_NamesTheShareOfTheWallClock(t *testing.T) {
	got := wfaProviderWait(analyzePhase{WallClockMs: 129000, ProviderWaitMs: 113000})
	if !strings.Contains(got, "87%") {
		t.Errorf("provider wait = %q, want the share of wall clock (87%%) — a bare duration buries the finding", got)
	}
	if !strings.Contains(got, "1m53s") {
		t.Errorf("provider wait = %q, want the duration too", got)
	}

	// A phase that was never rate limited must not grow a column of zeroes.
	if got := wfaProviderWait(analyzePhase{WallClockMs: 129000}); got != "-" {
		t.Errorf("un-limited phase = %q, want a dash", got)
	}
	// No wall clock to divide by: report the duration rather than a bogus share.
	if got := wfaProviderWait(analyzePhase{ProviderWaitMs: 2400}); got != "2s" {
		t.Errorf("phase with no wall clock = %q, want the bare duration", got)
	}
}
