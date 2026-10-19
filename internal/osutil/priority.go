// Copyright (c) 2025 Reliant Labs

package osutil

import "sync/atomic"

// ChildNiceValue is the Unix nice value applied to every process the daemon
// spawns on behalf of agent tool executions (foreground and background
// commands). Windows uses BELOW_NORMAL_PRIORITY_CLASS, the equivalent step.
//
// Many agents share one daemon, so a burst of builds, linters and test
// binaries can saturate every core of a developer machine. At that point the
// kernel has no reason to prefer the Docker VM's vCPU threads, the Electron
// UI or the daemon itself over the workload, and they starve (Docker's API
// stops answering, the UI freezes). Running the workload slightly below
// normal priority costs nothing when the machine is idle, since builds still
// use every free core, and makes it yield under contention. Children inherit
// the value on fork, so the build tools a shell spawns are covered too.
const ChildNiceValue = 10

// childPriorityNormal opts out of lowering child priority: the daemon's
// RELIANT_CHILD_PRIORITY=normal.
var childPriorityNormal atomic.Bool

// ConfigureChildPriority sets whether this process leaves its children at
// normal priority instead of lowering it. The daemon calls it once at boot,
// from its config; a process that never calls it lowers.
func ConfigureChildPriority(normal bool) { childPriorityNormal.Store(normal) }

func childPriorityDisabled() bool {
	return childPriorityNormal.Load()
}
