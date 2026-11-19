//go:build !linux

package resmetrics

import "errors"

// Phase 1 reads linux only. Elsewhere the /proc and cgroup readers are not
// pointed anywhere, so a sample carries what the Go runtime knows (the start
// time, goroutines, the runtime's mapped memory, the logical CPU count) and
// nothing is reported as failed.
const (
	defaultProcRoot   = ""
	defaultCgroupRoot = ""
	volumesSupported  = false
)

var errNoStatfs = errors.New("filesystem usage is read on linux only")

func statVolume(string) (dev, used, free uint64, err error) { return 0, 0, 0, errNoStatfs }
