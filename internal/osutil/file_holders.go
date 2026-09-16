// Copyright (c) 2025 Reliant Labs
package osutil

import "context"

// FileHoldState is the result of asking "does any live process currently hold
// this file open?".
//
// It is deliberately THREE-valued. A boolean would force the one answer that
// does damage: on a platform or in a configuration where the question cannot
// be answered, "false" reads as "nobody holds it, delete away", and deleting a
// file another process is actively writing is exactly the outcome the probe
// exists to prevent. Unknown keeps that mistake unrepresentable — callers are
// expected to treat it as "do not touch".
type FileHoldState int

const (
	// FileHoldUnknown means the probe could not answer. Callers must treat
	// this as "possibly held" and leave the file alone.
	FileHoldUnknown FileHoldState = iota
	// FileHoldHeld means at least one live process has the file open.
	FileHoldHeld
	// FileHoldNotHeld means the probe ran successfully and found no process
	// holding the file open.
	FileHoldNotHeld
)

func (s FileHoldState) String() string {
	switch s {
	case FileHoldHeld:
		return "held"
	case FileHoldNotHeld:
		return "not-held"
	default:
		return "unknown"
	}
}

// FileHolders reports whether any live process currently holds path open.
//
// This is the liveness signal behind stranded-lock recovery, and it is
// kernel-backed rather than inferred: the kernel closes every descriptor a
// process had open when it dies, whatever killed it — SIGKILL, OOM, power
// loss. So "no process has this file open" cannot be made stale by a process
// dying, and unlike a recorded pid it cannot be confused by pid reuse. It is
// the same principle internal/toolexec/daemonstate/lock.go relies on for
// daemon.lock, applied to a file we do not own and therefore cannot flock.
//
// Measured, and the reason this approach is viable at all: git DOES keep
// .git/index.lock open for as long as it holds it — lsof reports
// `git <pid> ... 3u REG ... index.lock` throughout a slow `git add`, and
// reports nothing for a lock left behind by a dead process.
//
// What it cannot tell you, stated plainly because the caller must compensate:
//
//   - A lock held by a process whose descriptor is on a DIFFERENT machine
//     (an NFS/SMB working copy) is invisible here. The probe is host-local.
//   - A process that created the lock and then closed it while still intending
//     to rename over it would look unheld. Git does not do this for
//     index.lock, but the caller should not assume a bare unheld reading is
//     proof on its own.
//   - On Windows there is no probe at all; the answer is always Unknown.
//
// Because of those gaps, an unheld answer is a necessary condition for
// removing a lock, never a sufficient one on its own. See
// gitutil.clearStrandedIndexLock for the corroboration layered on top.
func FileHolders(ctx context.Context, path string) FileHoldState {
	if path == "" {
		return FileHoldUnknown
	}
	return fileHolders(ctx, path)
}
