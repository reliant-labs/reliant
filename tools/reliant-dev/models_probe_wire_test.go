// Copyright (c) 2025 Reliant Labs
package main

import (
	"strings"
	"testing"
)

// A wire dump lands in research/, next to the code. Credentials in headers and
// account fingerprints in bodies must be gone before the bytes reach disk, in
// both shapes the providers use: Claude Code nests them in a JSON STRING
// (metadata.user_id, escaped quotes), Codex sends plain JSON.
func TestScrubWireRedactsCredentialsAndFingerprints(t *testing.T) {
	request := "POST /v1/messages HTTP/1.1\r\n" +
		"Authorization: Bearer sk-ant-oat01-secret\r\n" +
		"x-api-key: sk-or-v1-secret\r\n" +
		"chatgpt-account-id: acct-secret\r\n" +
		"x-claude-code-session-id: llm-probe\r\n\r\n" +
		`{"metadata":{"user_id":"{\"device_id\":\"660eda30cb\",\"account_uuid\":\"caa03712\",\"session_id\":\"llm-probe\"}"},` +
		`"client_metadata":{"installation_id":"f28f00aa","account_email":"someone@example.com"}}`

	got := string(scrubWire([]byte(request)))

	for _, leaked := range []string{"sk-ant-oat01-secret", "sk-or-v1-secret", "acct-secret", "660eda30cb", "caa03712", "f28f00aa", "someone@example.com"} {
		if strings.Contains(got, leaked) {
			t.Errorf("scrubbed dump still contains %q:\n%s", leaked, got)
		}
	}
	// Keys survive so a reader can see the field was present; only values go.
	for _, kept := range []string{`\"device_id\":\"[redacted]\"`, `\"account_uuid\":\"[redacted]\"`, `"installation_id":"[redacted]"`, `\"session_id\":\"llm-probe\"`, "x-claude-code-session-id: llm-probe"} {
		if !strings.Contains(got, kept) {
			t.Errorf("scrubbed dump lost %q:\n%s", kept, got)
		}
	}
}
