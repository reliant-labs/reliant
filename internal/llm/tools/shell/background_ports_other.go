// Copyright (c) 2025 Reliant Labs
//go:build !windows && !linux

package shell

// childPIDSnapshot is the parent PID -> child PIDs map of the whole process
// table. Off Linux there is no procfs to read it from, so it is one `ps`.
func childPIDSnapshot() map[int][]int { return psChildPIDSnapshot() }

// getPortsForPids is the TCP LISTEN ports of pids, keyed by owning PID. Off
// Linux it is one `lsof`.
func getPortsForPids(pids []int) (map[int][]PortInfo, error) { return lsofPortsForPids(pids) }
