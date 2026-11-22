package cli

import (
	"strings"
	"testing"
)

// `forge generate` printed "✅ Scaffolded internal/app/login_broker.go" on
// every run, including runs where the scaffold-once guard declined and the
// file did not exist before or after. A ✅ line is read as proof the file
// exists, so it prints only on the run that wrote it.
func TestGenerateLoginBroker_PrintsScaffoldedOnlyWhenWritten(t *testing.T) {
	const line = "Scaffolded internal/app/login_broker.go"
	dir := t.TempDir()

	run := func() string {
		t.Helper()
		var err error
		out := captureStdout(t, func() { err = generateLoginBroker("example.com/app", dir) })
		if err != nil {
			t.Fatalf("generateLoginBroker: %v", err)
		}
		return out
	}

	if first := run(); !strings.Contains(first, line) {
		t.Fatalf("the run that wrote login_broker.go did not say so; output:\n%s", first)
	}
	if second := run(); strings.Contains(second, line) {
		t.Fatalf("a run that wrote nothing claimed it scaffolded login_broker.go; output:\n%s", second)
	}
}
