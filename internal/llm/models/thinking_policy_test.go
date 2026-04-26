package models

import "testing"

func TestResolveThinkingCapability(t *testing.T) {
	tests := []struct {
		name    string
		caps    ModelCapabilities
		want    []string
		wantDef string
	}{
		{
			name:    "non reasoning model",
			caps:    ModelCapabilities{CanReason: false},
			want:    []string{},
			wantDef: "",
		},
		{
			name:    "gpt-5.5 xhigh levels",
			caps:    ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh"}},
			want:    []string{"low", "medium", "high", "xhigh"},
			wantDef: "medium",
		},
		{
			name:    "gpt-5.4-pro medium/high/xhigh",
			caps:    ModelCapabilities{CanReason: true, ThinkingLevels: []string{"medium", "high", "xhigh"}},
			want:    []string{"medium", "high", "xhigh"},
			wantDef: "medium",
		},
		{
			name:    "gemini low/high",
			caps:    ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "high"}},
			want:    []string{"low", "high"},
			wantDef: "high",
		},
		{
			name:    "default reasoning (empty thinking_levels)",
			caps:    ModelCapabilities{CanReason: true},
			want:    []string{"low", "medium", "high"},
			wantDef: "medium",
		},
		{
			name:    "explicit default levels",
			caps:    ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "medium", "high"}},
			want:    []string{"low", "medium", "high"},
			wantDef: "medium",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cap := ResolveThinkingCapability(tc.caps)
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
	cap := ResolveThinkingCapability(ModelCapabilities{
		CanReason:      true,
		ThinkingLevels: []string{"low", "medium", "high", "xhigh"},
	})
	if got := ReconcileThinkingLevel(cap, ""); got != "medium" {
		t.Fatalf("empty should reconcile to medium, got=%q", got)
	}
	if got := ReconcileThinkingLevel(cap, "xhigh"); got != "xhigh" {
		t.Fatalf("xhigh should remain xhigh, got=%q", got)
	}
	if got := ReconcileThinkingLevel(cap, "invalid"); got != "medium" {
		t.Fatalf("invalid should reconcile to medium, got=%q", got)
	}

	none := ResolveThinkingCapability(ModelCapabilities{CanReason: false})
	if got := ReconcileThinkingLevel(none, "high"); got != "" {
		t.Fatalf("non-reasoning should reconcile to empty, got=%q", got)
	}
}
