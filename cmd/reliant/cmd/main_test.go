// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"testing"
)

// TestMain clears the platform-stated daemon identity. These tests also run
// inside managed workspace pods, where RELIANT_DAEMON_TYPE=managed is exported;
// `daemon start`'s config reads it, so inheriting it would make every "this is
// a laptop" assumption false. Tests that need a managed daemon say so with
// t.Setenv.
//
// The instance settings go too: in a pod DAEMON_DATA_DIR names the live
// daemon's own data directory, and a daemon command a test runs without an
// explicit --data-dir must never land on it.
func TestMain(m *testing.M) {
	for _, name := range []string{
		"RELIANT_DAEMON_TYPE",
		"DAEMON_DATA_DIR",
		"RELIANT_INSTANCE_ACCOUNT",
		"RELIANT_INSTANCE_WORKSPACE",
	} {
		os.Unsetenv(name)
	}
	os.Exit(m.Run())
}
