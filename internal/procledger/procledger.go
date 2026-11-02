// Copyright (c) 2026 Reliant Labs

// Package procledger is this daemon's ledger of the processes it started whose
// whole filesystem footprint it knows without asking /proc.
//
// Today that is credsandbox's init. It makes itself undumpable, so its
// /proc/<pid>/fd reads EACCES for everyone, this daemon included, and
// internal/worktreereclaim cannot see what it holds open. But its code is the
// daemon's own, and what it holds is what the daemon handed it at start: its
// working directory, its executable, and the files on its stdio and extra
// descriptors. internal/proctree records that here when the init starts and
// forgets it once the daemon has reaped it, so a sandboxed command in one
// workspace does not hold every other workspace's retirement (reliant#1998).
//
// An entry names one process for good: a pid alone could be recycled into a
// stranger's process after the init is reaped, so it is matched together with
// the process group and the start time the kernel reports for it.
//
// The package has no dependencies, so the reader (worktreereclaim) need not
// import the writer (proctree) or anything the writer imports.
//
//forge:exclude-contract: one process-wide table of this daemon's own children; no second implementation
package procledger

import (
	"slices"
	"sync"
)

// Process is one accounted process.
type Process struct {
	// Pid is the process id, in the daemon's PID namespace.
	Pid int
	// Pgid is its process group, as the daemon set it at start.
	Pgid int
	// Start is field 22 of /proc/<pid>/stat: clock ticks from boot to the
	// process's start. With Pid it names one process, never a recycled pid.
	Start uint64
	// Paths is every filesystem path the process holds: its working
	// directory, its executable and each inherited file. Absolute.
	Paths []string
}

var (
	mu    sync.Mutex
	byPid = map[int]Process{}
)

// Record adds p, replacing any entry for its pid.
func Record(p Process) {
	p.Paths = slices.Clone(p.Paths)
	mu.Lock()
	byPid[p.Pid] = p
	mu.Unlock()
}

// Forget drops the entry for pid if it is the one that started at start: a
// late Forget for a reaped process never drops the entry of a newer process
// given the same pid.
func Forget(pid int, start uint64) {
	mu.Lock()
	if p, ok := byPid[pid]; ok && p.Start == start {
		delete(byPid, pid)
	}
	mu.Unlock()
}

// Lookup returns the entry recorded for pid. The caller must still compare
// Pgid and Start with what /proc reports before trusting it.
func Lookup(pid int) (Process, bool) {
	mu.Lock()
	defer mu.Unlock()
	p, ok := byPid[pid]
	if ok {
		p.Paths = slices.Clone(p.Paths)
	}
	return p, ok
}
