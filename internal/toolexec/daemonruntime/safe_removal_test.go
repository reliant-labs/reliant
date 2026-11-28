package daemonruntime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// requireSafeRemoval skips a test that expects a worktree reclaim, retirement
// or recovery read to succeed on a host whose kernel or sandbox lacks openat2
// or renameat2 (ENOSYS; some CI runners). worktreereclaim fails closed there:
// it holds every worktree and refuses every unsafe read. The host is missing a
// resource, so the test skips rather than fails.
func requireSafeRemoval(t *testing.T) {
	t.Helper()
	if err := worktreereclaim.HostLacksSafeRemoval(); err != nil {
		t.Skipf("host lacks openat2 (ENOSYS): %v; safe removal holds everything on this host, and this test expects a removal", err)
	}
}
