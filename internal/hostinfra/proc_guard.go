package hostinfra

import "github.com/reliant-labs/forge/internal/procguard"

// refuseLineage keeps host-infra shutdown inside the rule every forge stop
// path follows: never signal this command's own lineage (see procguard). A
// host-infra server is not an ancestor in any setup forge creates, but a
// recorded pid can be recycled into one, and an invariant that holds "in
// practice" on one path and by construction on the others is not one.
func refuseLineage(pid int) error {
	if procguard.Self().Contains(pid) {
		return &procguard.LineageError{PID: pid}
	}
	return nil
}
