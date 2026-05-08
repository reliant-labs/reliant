package models

import "testing"

func TestSupportedThinkingLevels(t *testing.T) {
	tests := []struct {
		name string
		caps ModelCapabilities
		want []string
	}{
		{name: "non-reasoning model", caps: ModelCapabilities{CanReason: false}, want: []string{}},
		{name: "xhigh levels", caps: ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh"}}, want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4-pro starts at medium", caps: ModelCapabilities{CanReason: true, ThinkingLevels: []string{"medium", "high", "xhigh"}}, want: []string{"medium", "high", "xhigh"}},
		{name: "gemini low/high only", caps: ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "high"}}, want: []string{"low", "high"}},
		{name: "default when empty", caps: ModelCapabilities{CanReason: true}, want: []string{"low", "medium", "high"}},
		{name: "explicit default levels", caps: ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "medium", "high"}}, want: []string{"low", "medium", "high"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SupportedThinkingLevels(tc.caps)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d want=%d; got=%v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got[%d]=%q want=%q; got=%v", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestSupportsThinkingLevelForCaps(t *testing.T) {
	xhighCaps := ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "medium", "high", "xhigh"}}
	defaultCaps := ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "medium", "high"}}
	noLowCaps := ModelCapabilities{CanReason: true, ThinkingLevels: []string{"medium", "high", "xhigh"}}
	lowHighCaps := ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "high"}}

	if !SupportsThinkingLevelForCaps(xhighCaps, "xhigh") {
		t.Fatal("expected xhigh caps to support xhigh")
	}
	if SupportsThinkingLevelForCaps(defaultCaps, "xhigh") {
		t.Fatal("expected default caps to reject xhigh")
	}
	if SupportsThinkingLevelForCaps(noLowCaps, "low") {
		t.Fatal("expected no-low caps to reject low")
	}
	if !SupportsThinkingLevelForCaps(noLowCaps, "xhigh") {
		t.Fatal("expected no-low caps to support xhigh")
	}
	if SupportsThinkingLevelForCaps(lowHighCaps, "medium") {
		t.Fatal("expected low/high caps to reject medium")
	}
	if !SupportsThinkingLevelForCaps(defaultCaps, "") {
		t.Fatal("expected empty level to be accepted")
	}
}

func TestReconcileThinkingLevelDisablesUnsupportedNonReasoningModels(t *testing.T) {
	cap := ResolveThinkingCapability(ModelCapabilities{CanReason: false})
	if got := ReconcileThinkingLevel(cap, "medium"); got != "" {
		t.Fatalf("non-reasoning model reconciled medium to %q, want disabled", got)
	}
}
