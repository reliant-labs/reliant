// Copyright (c) 2025 Reliant Labs

package osutil

import "os"

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

// ChildPriorityEnv, set to "normal", opts out of lowering child priority.
const ChildPriorityEnv = "RELIANT_CHILD_PRIORITY"

func childPriorityDisabled() bool {
	return os.Getenv(ChildPriorityEnv) == "normal"
}
