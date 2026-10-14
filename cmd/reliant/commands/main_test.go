// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// TestMain clears the platform-stated daemon identity. These tests also run
// inside managed workspace pods, where RELIANT_DAEMON_TYPE=managed is exported;
// code under test reads it (daemonruntime.IsManagedEnvironment), so inheriting
// it would make every "this is a laptop" assumption false. Tests that need a
// managed daemon say so with t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv(daemonruntime.DaemonTypeEnvVar)
	os.Exit(m.Run())
}
