//go:build !unix

package selfupdate

// Windows (and every other non-unix target) can stage a release but not swap
// it yet: a running .exe can be renamed but not replaced, and there is no
// exec(2). The Windows swap is phase F2 of docs/proposals/self-upgrade.md.

func swapSupported() bool { return false }

func execBinary(string, []string, []string) error { return ErrUnsupported }

func isTextBusy(error) bool { return false }

// processAlive is conservative where it cannot ask: a boot it cannot see
// die is never counted as a crash.
func processAlive(int) bool { return true }

// syncDir is a no-op: Windows cannot open a directory for FlushFileBuffers
// without backup semantics, and NTFS journals the rename itself.
func syncDir(string) error { return nil }
