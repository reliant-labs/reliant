package models

import "testing"

func TestSupportedThinkingLevelsForModelDriver(t *testing.T) {
	tests := []struct {
		name      string
		canReason bool
		modelID   string
		driver    string
		want      []string
	}{
		{name: "non-reasoning model", canReason: false, modelID: "claude-4.5-haiku", driver: "anthropic", want: []string{}},
		{name: "gpt-5.5 codex supports xhigh", canReason: true, modelID: "gpt-5.5", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.5 openai supports xhigh", canReason: true, modelID: "gpt-5.5", driver: "openai", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4 codex supports xhigh", canReason: true, modelID: "gpt-5.4", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4 openai supports xhigh", canReason: true, modelID: "gpt-5.4", driver: "openai", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4-mini codex supports xhigh", canReason: true, modelID: "gpt-5.4-mini", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4-mini openai supports xhigh", canReason: true, modelID: "gpt-5.4-mini", driver: "openai", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4-mini openrouter supports xhigh", canReason: true, modelID: "gpt-5.4-mini", driver: "openrouter", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4-pro openai starts at medium", canReason: true, modelID: "gpt-5.4-pro", driver: "openai", want: []string{"medium", "high", "xhigh"}},
		{name: "gpt-5.3-codex supports xhigh", canReason: true, modelID: "gpt-5.3-codex", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.3-codex-spark supports xhigh", canReason: true, modelID: "gpt-5.3-codex-spark", driver: "codex", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.2-codex openai max high", canReason: true, modelID: "gpt-5.2-codex", driver: "openai", want: []string{"low", "medium", "high"}},
		{name: "gemini 3 pro is low high only", canReason: true, modelID: "gemini-3.1-pro-preview", driver: "gemini", want: []string{"low", "high"}},
		{name: "anthropic max high by default", canReason: true, modelID: "claude-4.6-opus", driver: "anthropic", want: []string{"low", "medium", "high"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SupportedThinkingLevelsForModelDriver(tc.canReason, tc.modelID, tc.driver)
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

func TestSupportsThinkingLevelForModelDriver(t *testing.T) {
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.5", "codex", "xhigh") {
		t.Fatal("expected gpt-5.5 on codex to support xhigh")
	}
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.5", "openai", "xhigh") {
		t.Fatal("expected gpt-5.5 on openai to support xhigh")
	}
	if SupportsThinkingLevelForModelDriver(true, "gpt-5.2-codex", "openai", "xhigh") {
		t.Fatal("expected gpt-5.2-codex on openai to reject xhigh")
	}
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.2-codex", "codex", "xhigh") {
		t.Fatal("expected gpt-5.2-codex on codex to support xhigh")
	}
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.3-codex-spark", "codex", "xhigh") {
		t.Fatal("expected gpt-5.3-codex-spark on codex to support xhigh")
	}
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.4-mini", "codex", "xhigh") {
		t.Fatal("expected gpt-5.4-mini on codex to support xhigh")
	}
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.4-mini", "openai", "xhigh") {
		t.Fatal("expected gpt-5.4-mini on openai to support xhigh")
	}
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.4-mini", "openrouter", "xhigh") {
		t.Fatal("expected gpt-5.4-mini on openrouter to support xhigh")
	}
	if SupportsThinkingLevelForModelDriver(true, "gpt-5.4-pro", "openai", "low") {
		t.Fatal("expected gpt-5.4-pro on openai to reject low")
	}
	if !SupportsThinkingLevelForModelDriver(true, "gpt-5.4-pro", "openai", "xhigh") {
		t.Fatal("expected gpt-5.4-pro on openai to support xhigh")
	}
	if SupportsThinkingLevelForModelDriver(true, "gemini-3.1-pro-preview", "gemini", "medium") {
		t.Fatal("expected gemini-3.1-pro-preview on gemini to reject medium")
	}
	if !SupportsThinkingLevelForModelDriver(true, "claude-4.6-opus", "anthropic", "") {
		t.Fatal("expected empty level to be accepted")
	}
}
