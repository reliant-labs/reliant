package generator

import "testing"

func TestParseHarness(t *testing.T) {
	tests := []struct {
		input   string
		want    Harness
		wantErr bool
	}{
		{"reliant", HarnessReliant, false},
		{"claude", HarnessClaude, false},
		{"cursor", HarnessCursor, false},
		{"copilot", HarnessCopilot, false},
		{"codex", HarnessCodex, false},
		{"", HarnessReliant, false},
		{"unknown", "", true},
		{"Claude", "", true}, // case-sensitive
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseHarness(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseHarness(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ParseHarness(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHarnessMemoryFilePath(t *testing.T) {
	tests := []struct {
		harness Harness
		want    string
	}{
		{HarnessReliant, "reliant.md"},
		{HarnessClaude, "CLAUDE.md"},
		{HarnessCursor, ".cursorrules"},
		{HarnessCopilot, ".github/copilot-instructions.md"},
		{HarnessCodex, "AGENTS.md"},
		{"", "reliant.md"}, // zero-value defaults to reliant
	}
	for _, tt := range tests {
		t.Run(string(tt.harness), func(t *testing.T) {
			got := tt.harness.MemoryFilePath()
			if got != tt.want {
				t.Fatalf("Harness(%q).MemoryFilePath() = %q, want %q", tt.harness, got, tt.want)
			}
		})
	}
}
