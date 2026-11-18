package hostinfra

import "testing"

// TestPostgresStartParameters_SharedMemoryOverrideIsPOSIXOnly pins the
// per-platform split in postgresStartParameters. mmap is the macOS SHMMNI fix
// and must stay on every POSIX host; on Windows postgres accepts only
// "windows" for both settings and refuses to boot on anything else, so the
// override made every `forge env up` postgres fail there.
func TestPostgresStartParameters_SharedMemoryOverrideIsPOSIXOnly(t *testing.T) {
	shmKeys := []string{"dynamic_shared_memory_type", "shared_memory_type"}

	for _, goos := range []string{"darwin", "linux"} {
		params := postgresStartParameters(goos)
		for _, key := range shmKeys {
			if got := params[key]; got != "mmap" {
				t.Errorf("%s: %s = %q, want mmap (sysv exhausts SHMMNI on macOS)", goos, key, got)
			}
		}
	}

	windows := postgresStartParameters("windows")
	for _, key := range shmKeys {
		if got, set := windows[key]; set {
			t.Errorf("windows: %s overridden to %q; its postgres accepts only \"windows\" and will not start", key, got)
		}
	}
	if windows["shared_buffers"] != "64MB" || windows["max_connections"] != "100" {
		t.Errorf("windows lost the platform-independent overrides: %v", windows)
	}
}
