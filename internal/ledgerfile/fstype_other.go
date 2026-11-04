//go:build !linux && !darwin

package ledgerfile

// platformFSType cannot identify the filesystem on this platform, so every
// path is admitted (see fsTypeFunc). Windows holds its locks through
// LockFileEx on the handle, and a remote SMB share there enforces them
// server-side, so the Unix "advisory lock may be local only" hazard does not
// apply in the same way.
func platformFSType(string) (string, error) { return "", nil }
