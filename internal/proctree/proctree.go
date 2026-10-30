// Copyright (c) 2026 Reliant Labs

// Package proctree bounds how many processes one command the daemon runs may
// create, so a runaway command fails alone instead of taking every agent on the
// machine down with it.
//
// Why. On 2026-10-30 a cloud machine hit its process limit (~5,200 bash, load
// ~1,900, fork EAGAIN everywhere) and had to be restarted, killing every
// agent's in-flight work: one agent's test re-ran a CI gate inside itself
// without bound. Every agent's shell runs as the same user, through one
// daemon, so the only limit that applied was the user's RLIMIT_NPROC (7,650
// there) — shared by all of them AND by the daemon. The first command to reach
// it took the rest down.
//
// What. Each command the daemon starts is a Tree:
//
//   - On Linux with a delegated cgroup v2 subtree (the cloud workspace
//     entrypoint delegates the daemon's cgroup to the workspace user), the
//     command is born — clone3(CLONE_INTO_CGROUP), so no fork escapes — into
//     a cgroup of its own whose pids.max is PidsMax (2,000 by default, at
//     most a third of the user's process limit). The tree's 2,001st process
//     fails to fork; nothing else does. The cgroup also lets a kill reach
//     every process the command started, including any that left its process
//     group with setsid (cgroup.kill).
//   - Wherever the user's process limit is finite (Linux), the command's
//     RLIMIT_NPROC is lowered to a share of it (ReserveFraction), applied with
//     prlimit as soon as the command has started. RLIMIT_NPROC counts every
//     process of the user, so this is not a per-command bound: it is a
//     reserve. Commands start failing to fork before the daemon does, so the
//     daemon can still run shell_kill, report and recover.
//
// Both degrade to nothing (only the process group) where unavailable — macOS,
// Windows, a cgroup that is not delegated — and never stop a command from
// starting: a broken limiter must never stop work.
//
//forge:exclude-contract: one concrete policy over exec.Cmd values and the daemon's own cgroup v2 subtree; local /proc and /sys only, no second implementation
package proctree

import (
	"fmt"
	"os/exec"
	"sync"
)

// DefaultPidsMax is the per-command process limit where cgroups allow one: well
// past any real build or test lane (a full `go test ./...` of this repository
// peaks in the hundreds), well short of a machine's whole budget.
const DefaultPidsMax = 2000

// ReserveFraction is the share of the user's process limit a command may use
// before its forks fail; the rest is the daemon's reserve.
const ReserveFraction = 0.85

// Limiter starts commands as bounded process trees.
type Limiter struct {
	// root is the delegated cgroup directory command cgroups are created in;
	// "" when there is none (why says so).
	root    string
	why     string
	pidsMax int
	// nproc is the RLIMIT_NPROC a command gets; 0 leaves it alone.
	nproc uint64

	mu  sync.Mutex
	seq uint64
}

// Config is the limiter's policy. The zero value detects everything.
type Config struct {
	// PidsMax overrides the per-command cgroup limit (0: the default).
	PidsMax int
	// CgroupRoot overrides the detected delegated cgroup directory; tests
	// use it. "-" turns cgroups off.
	CgroupRoot string
}

var (
	defaultMu sync.RWMutex
	defaultL  *Limiter
)

// SetDefault installs the limiter the daemon's exec paths use.
func SetDefault(l *Limiter) {
	defaultMu.Lock()
	defaultL = l
	defaultMu.Unlock()
}

// Default is the limiter SetDefault installed, or nil (which bounds nothing:
// a nil *Limiter's Prepare only sets the process group).
func Default() *Limiter {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultL
}

// String describes the limiter for a startup log line.
func (l *Limiter) String() string {
	if l == nil {
		return "commands run unbounded (no process limiter)"
	}
	s := "commands run unbounded per tree: " + l.why
	if l.root != "" {
		s = fmt.Sprintf("each command runs in a cgroup of its own under %s (pids.max %d)", l.root, l.pidsMax)
	}
	if l.nproc > 0 {
		s += fmt.Sprintf("; RLIMIT_NPROC %d per command (the rest is the daemon's reserve)", l.nproc)
	}
	return s
}

// Owner is who a command runs for, so a machine running out of processes can
// say whose processes they are.
type Owner struct {
	// ChatID is the agent (chat or sub-agent thread) whose tool call started
	// the command; "" when the caller did not say.
	ChatID string
	// Command is the command line as the caller gave it.
	Command string
}

// Tree is one command's process tree. Its methods are safe on a nil *Tree.
type Tree struct {
	l     *Limiter
	owner Owner
	dir   string // the command's cgroup, "" when it has none
	fd    int    // the cgroup directory, open until the command starts; -1 when none
	pid   int
}

// Prepare readies cmd, which must not have been started, to run as a bounded
// tree: its own process group always, and its own cgroup when the limiter has
// one. Call Started once cmd.Start returns, and Release once the command is
// done with. A failure to set up the cgroup leaves the command unbounded
// rather than failing it.
func (l *Limiter) Prepare(cmd *exec.Cmd, owner Owner) *Tree {
	t := &Tree{l: l, owner: owner, fd: -1}
	setProcessGroup(cmd)
	if l == nil || l.root == "" {
		return t
	}
	l.mu.Lock()
	l.seq++
	seq := l.seq
	l.mu.Unlock()
	t.prepareCgroup(cmd, seq)
	return t
}

// Started records the started command (cmd.Process.Pid) and applies what can
// only be applied to a running process. Call it whether or not Start
// succeeded (pid 0 when it did not).
func (t *Tree) Started(pid int) {
	if t == nil {
		return
	}
	t.closeFD()
	t.pid = pid
	if pid <= 0 {
		t.Release()
		return
	}
	if t.l != nil && t.l.nproc > 0 {
		t.limitNproc()
	}
	register(t)
}

// Cgroup is the tree's cgroup directory, "" when it has none.
func (t *Tree) Cgroup() string {
	if t == nil {
		return ""
	}
	return t.dir
}

// Info describes a live tree: its process group (the leader's pid), cgroup
// and owner.
type Info struct {
	Pgid   int
	Cgroup string
	Owner  Owner
}

// The trees started and not yet forgotten, by process group. A tree stays
// here after its command exits for as long as anything it left is alive:
// those processes are still its owner's. Forget drops it.
var (
	liveMu sync.Mutex
	live   = map[int]*Tree{}
)

func register(t *Tree) {
	// Only where a capacity guard scans /proc and prunes the list
	// (internal/procguard); elsewhere nothing would ever take entries out.
	if !trackLive {
		return
	}
	liveMu.Lock()
	live[t.pid] = t
	liveMu.Unlock()
}

// Live lists the trees started and not forgotten.
func Live() []Info {
	liveMu.Lock()
	defer liveMu.Unlock()
	out := make([]Info, 0, len(live))
	for pgid, t := range live {
		out = append(out, Info{Pgid: pgid, Cgroup: t.dir, Owner: t.owner})
	}
	return out
}

// Prune forgets every tree whose process group alive reports gone and whose
// cgroup, if it has one, is empty: nothing of the command is left to own.
func Prune(alive func(pgid int) bool) {
	liveMu.Lock()
	defer liveMu.Unlock()
	for pgid, t := range live {
		if !alive(pgid) && !t.Populated() {
			delete(live, pgid)
		}
	}
}
