//go:build (!unix && !windows) || aix

package hostslot

import "os"

// Where x/sys/unix has no flock(2) and there is no LockFileEx (Plan 9, wasm,
// AIX) a slot cannot exclude anyone: every tryLock succeeds, so heavy work is
// never queued there. This keeps the package building on every target; the
// limit holds only where lock_unix.go or lock_windows.go applies.
func tryLock(*os.File) (bool, error) { return true, nil }

func unlock(*os.File) error { return nil }
