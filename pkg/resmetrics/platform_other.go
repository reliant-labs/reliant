//go:build !linux && !darwin && !windows

package resmetrics

import "errors"

// Elsewhere (the BSDs, plan9, wasm, ...) there is no reader: a sample carries
// what the Go runtime knows (the start time, goroutines, the runtime's mapped
// memory, the logical CPU count) and nothing is reported as failed.
const (
	defaultProcRoot   = ""
	defaultCgroupRoot = ""
	volumesSupported  = false
	nativeReaders     = false
)

var errNoStatfs = errors.New("filesystem usage is not read on this platform")

func statVolume(string) (dev, used, free uint64, err error) { return 0, 0, 0, errNoStatfs }

func readProcess(*ProcessStats, func(string, error)) {}
func readHost(*HostStats, bool, func(string, error)) {}
