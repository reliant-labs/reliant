package cli

import (
	"os"
	"testing"
)

// requireProcInspection skips when this platform cannot read a process's
// environment — the marker mechanism, and therefore every ownership decision
// built on it, depends on that. It probes capability rather than switching on
// GOOS, so a platform that gains an implementation un-skips automatically.
func requireProcInspection(t *testing.T) {
	t.Helper()
	if _, ok := readProcEnviron(os.Getpid()); !ok {
		t.Skip("process-env inspection is unreadable on this platform")
	}
}
