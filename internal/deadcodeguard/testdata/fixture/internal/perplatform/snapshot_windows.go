package perplatform

// Take is the only production writer of Snapshot's fields, and it exists only
// in the Windows build.
func Take(pid int, created int64) Snapshot {
	return Snapshot{PID: pid, Created: created}
}
