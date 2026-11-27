//go:build !linux

package worktreereclaim

// HostLacksSafeRemoval is nil off Linux: there mount-safe removal is
// unsupported by design (it refuses and holds every worktree), not because
// the host is missing a syscall.
func HostLacksSafeRemoval() error { return nil }
