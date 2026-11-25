// Copyright (c) 2026 Reliant Labs
package osutil

// GroupMember is one live process of a process group, identified across pid
// reuse by its start time (clock ticks since boot).
//
// Why identity and not just a group id. Signalling -pgid reaches whatever group
// has that id NOW. While any member of the group a command started is alive,
// that id cannot be reused (the kernel does not hand out a pid that is still in
// use as a group id), so it is still the command's. Once they have all exited
// it can be — a later command's group, another agent's workload. So before a
// group is signalled after its leader is gone, some member recorded while the
// group was known to be the command's must be found alive, unchanged, still in
// it (GroupOwned). That member proves the id was never released.
type GroupMember struct {
	PID   int
	Start uint64
}
