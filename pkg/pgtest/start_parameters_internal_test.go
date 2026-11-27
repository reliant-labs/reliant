package pgtest

import "testing"

// TestStartParameters_SharedMemoryOverrideIsPOSIXOnly pins the per-platform
// split in startParameters. mmap is the macOS SHMMNI fix and must stay on
// every POSIX host; on Windows postgres accepts only "windows" for both
// settings and refuses to boot on anything else, which is what failed every
// embedded server on the Windows CI job.
func TestStartParameters_SharedMemoryOverrideIsPOSIXOnly(t *testing.T) {
	shmKeys := []string{"dynamic_shared_memory_type", "shared_memory_type"}

	for _, goos := range []string{"darwin", "linux"} {
		params := startParameters(goos)
		for _, key := range shmKeys {
			if got := params[key]; got != "mmap" {
				t.Errorf("%s: %s = %q, want mmap (sysv exhausts SHMMNI on macOS)", goos, key, got)
			}
		}
	}

	windows := startParameters("windows")
	for _, key := range shmKeys {
		if got, set := windows[key]; set {
			t.Errorf("windows: %s overridden to %q; its postgres accepts only \"windows\" and will not start", key, got)
		}
	}
	if windows["fsync"] != "off" || windows["max_connections"] != "200" {
		t.Errorf("windows lost the platform-independent overrides: %v", windows)
	}
}
