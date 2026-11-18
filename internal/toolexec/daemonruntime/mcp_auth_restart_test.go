package daemonruntime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// #2818: a browser tool whose page said "Authentication failed" — an agent
// testing a login form — read as the MCP server's own credentials failing, so
// the daemon removed chrome-devtools (every chat's browser) and started it
// again. Only a failed call of a server with credentials may trigger it.
func TestRestartsMCPServerOnAuthError(t *testing.T) {
	failed := func(text string) *toolexec.ExecutionResult {
		return &toolexec.ExecutionResult{Success: true, IsError: true, Content: text}
	}
	succeeded := func(text string) *toolexec.ExecutionResult {
		return &toolexec.ExecutionResult{Success: true, Content: text}
	}

	cases := []struct {
		name   string
		server string
		result *toolexec.ExecutionResult
		want   bool
	}{
		{"failed call of a credentialed server", "github", failed("401: Bad credentials"), true},
		{"transport failure of a credentialed server", "github",
			&toolexec.ExecutionResult{Success: false, IsError: true, ErrorMessage: "authentication failed"}, true},
		{"successful output that mentions auth", "github", succeeded("issue #4: Authentication failed on login"), false},
		{"browser page that failed sign-in", "chrome-devtools", failed("Error: Authentication failed"), false},
		{"browser page text", "chrome-devtools", succeeded("<h1>Invalid API key</h1>"), false},
		{"unrelated failure", "github", failed("rate limited"), false},
		{"no server", "", failed("authentication failed"), false},
		{"no result", "github", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := restartsMCPServerOnAuthError(tc.server, tc.result); got != tc.want {
				t.Fatalf("restartsMCPServerOnAuthError(%q) = %v, want %v", tc.server, got, tc.want)
			}
		})
	}
}
