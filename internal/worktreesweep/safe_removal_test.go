package worktreesweep

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// requireSafeRemoval skips a test that expects a sweep to remove or retire a
// worktree on a host whose kernel or sandbox lacks openat2 or renameat2
// (ENOSYS; some CI runners). worktreereclaim fails closed there and holds
// everything: the host is missing a resource, so the test skips, not fails.
func requireSafeRemoval(t *testing.T) {
	t.Helper()
	if err := worktreereclaim.HostLacksSafeRemoval(); err != nil {
		t.Skipf("host lacks openat2 (ENOSYS): %v; safe removal holds everything on this host, and this test expects a removal", err)
	}
}
