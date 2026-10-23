// Package perplatform plants the two shapes that only a scan of EVERY target
// OS judges correctly. A scan of the host alone cannot see a _windows.go file
// on macOS or Linux, so without the per-OS loads both would be false findings.
package perplatform

// Snapshot's fields are written only by the Windows build (snapshot_windows.go)
// and read here on every OS. OK: written in production — on Windows.
type Snapshot struct {
	PID     int
	Created int64
}

// Describe reads every field.
func Describe(s Snapshot) int64 { return int64(s.PID) + s.Created }

// Lock has a real body on Unix (lock_unix.go) and a stub on Windows
// (lock_windows.go). OK: a per-OS seam, not a no-op.
func UseLock(fd int) error { return lock(fd) }

// Probe is a stub on EVERY OS (probe_unix.go, probe_windows.go).
// WANT noop-func: no platform does anything with its parameter.
func UseProbe(path string) error { return probe(path) }
