package models

import "testing"

func TestResolveThinkingCapability(t *testing.T) {
	tests := []struct {
		name      string
		canReason bool
		modelID   string
		driver    string
		want      []string
		wantDef   string
	}{
		{name: "non reasoning model", canReason: false, modelID: "claude-4.5-haiku", driver: "anthropic", want: []string{}, wantDef: ""},
		{name: "gpt-5.5 codex xhigh", canReason: true, modelID: "gpt-5.5", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}, wantDef: "medium"},
		{name: "gpt-5.4 codex xhigh", canReason: true, modelID: "gpt-5.4", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}, wantDef: "medium"},
		{name: "gpt-5.4-pro openai medium/high/xhigh", canReason: true, modelID: "gpt-5.4-pro", driver: "openai", want: []string{"medium", "high", "xhigh"}, wantDef: "medium"},
		{name: "gpt-5.4-mini codex xhigh", canReason: true, modelID: "gpt-5.4-mini", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}, wantDef: "medium"},
		{name: "codex xhigh", canReason: true, modelID: "gpt-5.3-codex", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}, wantDef: "medium"},
		{name: "codex spark xhigh", canReason: true, modelID: "gpt-5.3-codex-spark", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}, wantDef: "medium"},
		{name: "gemini 3 pro low/high", canReason: true, modelID: "gemini-3.1-pro-preview", driver: "gemini", want: []string{"low", "high"}, wantDef: "high"},
		{name: "default reasoning", canReason: true, modelID: "claude-4.6-opus", driver: "anthropic", want: []string{"low", "medium", "high"}, wantDef: "medium"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cap := ResolveThinkingCapability(tc.canReason, tc.modelID, tc.driver)
			if len(cap.Levels) != len(tc.want) {
				t.Fatalf("levels len=%d want=%d got=%v", len(cap.Levels), len(tc.want), cap.Levels)
			}
			for i := range tc.want {
				if cap.Levels[i] != tc.want[i] {
					t.Fatalf("levels[%d]=%q want=%q", i, cap.Levels[i], tc.want[i])
				}
			}
			if cap.DefaultLevel != tc.wantDef {
				t.Fatalf("default=%q want=%q", cap.DefaultLevel, tc.wantDef)
			}
		})
	}
}

func TestReconcileThinkingLevel(t *testing.T) {
	cap := ResolveThinkingCapability(true, "gpt-5.3-codex-spark", "codex")
	if got := ReconcileThinkingLevel(cap, ""); got != "medium" {
		t.Fatalf("empty should reconcile to medium, got=%q", got)
	}
	if got := ReconcileThinkingLevel(cap, "xhigh"); got != "xhigh" {
		t.Fatalf("xhigh should remain xhigh, got=%q", got)
	}
	if got := ReconcileThinkingLevel(cap, "invalid"); got != "medium" {
		t.Fatalf("invalid should reconcile to medium, got=%q", got)
	}

	none := ResolveThinkingCapability(false, "claude-4.5-haiku", "anthropic")
	if got := ReconcileThinkingLevel(none, "high"); got != "" {
		t.Fatalf("non-reasoning should reconcile to empty, got=%q", got)
	}
}
