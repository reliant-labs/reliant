// Copyright (c) 2026 Reliant Labs
package osutil

// ProcessIdentity is what tells a process from a later holder of its pid: a
// pid is reused once its process is reaped, its start time is not. Start is
// clock ticks since boot on Linux (/proc/<pid>/stat field 22) and
// microseconds since the epoch on macOS (kinfo_proc p_starttime), the same
// unit GroupMember.Start uses; compare it only with one read on this machine.
type ProcessIdentity struct {
	PPID  int
	PGID  int
	Start uint64
	// Exited is a zombie: the process has exited and its parent has not
	// reaped it yet, so its exit status is still there to collect.
	Exited bool
}

// Same reports whether id is the process recorded as start: the same start
// time, which a reused pid cannot have. A zero start was never recorded and
// proves nothing.
func (id ProcessIdentity) Same(start uint64) bool {
	return start != 0 && id.Start == start
}
