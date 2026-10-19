// Copyright (c) 2025 Reliant Labs

package osutil

import "testing"

// keepChildPriorityForTest leaves children at normal priority for the length
// of a test, as a daemon configured with RELIANT_CHILD_PRIORITY=normal does.
func keepChildPriorityForTest(t *testing.T) {
	t.Helper()
	prev := childPriorityNormal.Load()
	ConfigureChildPriority(true)
	t.Cleanup(func() { ConfigureChildPriority(prev) })
}
