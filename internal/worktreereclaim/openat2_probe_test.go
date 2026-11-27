package worktreereclaim

import "testing"

// requireOpenat2 skips a test that needs openat2 or renameat2 on a host whose
// kernel or sandbox does not implement them (some CI runners' seccomp profiles
// answer ENOSYS; HostLacksSafeRemoval). The host is missing a resource, so the
// test skips rather than fails.
func requireOpenat2(t testing.TB) {
	t.Helper()
	if err := HostLacksSafeRemoval(); err != nil {
		t.Skipf("host lacks openat2 (ENOSYS): %v; the reaper holds every worktree on this host, and this test needs the syscall", err)
	}
}
