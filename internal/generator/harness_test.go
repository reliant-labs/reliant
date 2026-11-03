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

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetectHarness(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		want       Harness
		wantMarker string
	}{
		{"nothing set", nil, "", ""},
		{"reliant", map[string]string{"RELIANT_AGENT_SHELL": "1"}, HarnessReliant, "RELIANT_AGENT_SHELL"},
		{"claude code", map[string]string{"CLAUDECODE": "1"}, HarnessClaude, "CLAUDECODE"},
		{"codex thread", map[string]string{"CODEX_THREAD_ID": "abc"}, HarnessCodex, "CODEX_THREAD_ID"},
		{"codex session", map[string]string{"CODEX_SESSION_ID": "abc"}, HarnessCodex, "CODEX_SESSION_ID"},
		{"cursor", map[string]string{"CURSOR_AGENT": "1"}, HarnessCursor, "CURSOR_AGENT"},
		{"explicit zero is not a marker", map[string]string{"CLAUDECODE": "0"}, "", ""},
		// Codex's own user config also exports CODEX_HOME; only the
		// per-command markers identify a running agent.
		{"codex home alone is not a marker", map[string]string{"CODEX_HOME": "/x"}, "", ""},
		// A reliant daemon started from a Claude Code terminal inherits
		// CLAUDECODE; the innermost agent (reliant) is the one running us.
		{"reliant wins over inherited claude", map[string]string{"RELIANT_AGENT_SHELL": "1", "CLAUDECODE": "1"}, HarnessReliant, "RELIANT_AGENT_SHELL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, marker := DetectHarness(envOf(tt.env))
			if got != tt.want || marker != tt.wantMarker {
				t.Fatalf("DetectHarness = (%q, %q), want (%q, %q)", got, marker, tt.want, tt.wantMarker)
			}
		})
	}
}

func TestResolveHarnessPrecedence(t *testing.T) {
	claudeEnv := envOf(map[string]string{"CLAUDECODE": "1"})
	tests := []struct {
		name         string
		flag         string
		env          func(string) string
		want         Harness
		wantDetected string
		wantErr      bool
	}{
		{"flag beats detection", "cursor", claudeEnv, HarnessCursor, "", false},
		{"explicit reliant beats detection", "reliant", claudeEnv, HarnessReliant, "", false},
		{"detection beats default", "", claudeEnv, HarnessClaude, "CLAUDECODE", false},
		{"default when neither", "", envOf(nil), HarnessReliant, "", false},
		{"bad flag is an error even when detected", "nope", claudeEnv, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, detected, err := ResolveHarness(tt.flag, tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveHarness error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want || detected != tt.wantDetected {
				t.Fatalf("ResolveHarness = (%q, %q), want (%q, %q)", got, detected, tt.want, tt.wantDetected)
			}
		})
	}
}
