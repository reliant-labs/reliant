package buildinfo

import "testing"

func TestDetectAgent(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		want       string
		wantMarker string
	}{
		{"nothing set", nil, "", ""},
		{"reliant", map[string]string{"RELIANT_AGENT_SHELL": "1"}, AgentReliant, "RELIANT_AGENT_SHELL"},
		{"claude code", map[string]string{"CLAUDECODE": "1"}, AgentClaude, "CLAUDECODE"},
		{"codex thread", map[string]string{"CODEX_THREAD_ID": "abc"}, AgentCodex, "CODEX_THREAD_ID"},
		{"cursor", map[string]string{"CURSOR_AGENT": "1"}, AgentCursor, "CURSOR_AGENT"},
		{"gemini", map[string]string{"GEMINI_CLI": "1"}, AgentGemini, "GEMINI_CLI"},
		{"explicit zero is not a marker", map[string]string{"GEMINI_CLI": "0"}, "", ""},
		{"reliant wins over inherited claude", map[string]string{"RELIANT_AGENT_SHELL": "1", "CLAUDECODE": "1"}, AgentReliant, "RELIANT_AGENT_SHELL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, marker := DetectAgent(func(k string) string { return tt.env[k] })
			if got != tt.want || marker != tt.wantMarker {
				t.Fatalf("DetectAgent = (%q, %q), want (%q, %q)", got, marker, tt.want, tt.wantMarker)
			}
		})
	}
}
