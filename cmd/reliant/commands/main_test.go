// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"testing"
)

// TestMain clears the platform-stated daemon identity. These tests also run
// inside managed workspace pods, where RELIANT_DAEMON_TYPE=managed is exported;
// `daemon start`'s config reads it, so inheriting it would make every "this is
// a laptop" assumption false. Tests that need a managed daemon say so with
// t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("RELIANT_DAEMON_TYPE")
	os.Exit(m.Run())
}
