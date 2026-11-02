// Copyright (c) 2026 Reliant Labs
//go:build !linux

package proctree

import "os/exec"

// trackLive is off: no /proc scan prunes a list of live trees here.
const trackLive = false

// New builds a limiter. Outside Linux there is no cgroup v2 and no
// per-command process limit to set: commands only get their process group.
func New(cfg Config) *Limiter {
	return &Limiter{why: "per-command limits need Linux cgroup v2"}
}

func (t *Tree) prepareCgroup(*exec.Cmd, uint64) {}

func (t *Tree) closeFD() {}

func (t *Tree) limitNproc() {}

// Kill kills nothing outside Linux; the caller kills the process group.
func (t *Tree) Kill() bool { return false }

// limitNote is "" outside Linux: no limit refuses a command anything.
func (t *Tree) limitNote() string { return "" }

// Populated is false outside Linux.
func (t *Tree) Populated() bool { return false }

// Release does nothing else outside Linux.
func (t *Tree) Release() { t.forget() }

// processIdentity is unknown outside Linux, so nothing enters the ledger.
func processIdentity(int) (pgid int, start uint64, ok bool) { return 0, 0, false }
