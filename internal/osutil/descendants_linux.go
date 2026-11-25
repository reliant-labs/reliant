// Copyright (c) 2026 Reliant Labs
//go:build linux

package osutil

import (
	"errors"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// descendants is the process tree of a cancelled command, kept in custody
// through the grace period so the escalation reaches all of it.
//
// It holds every process of the command it can prove is the command's: its
// own process group, the groups its descendants made of their own (strays:
// `timeout` takes one unless given --foreground, and setsid makes one), and
// descendants loose in someone else's group. A signal to the command's group
// never reaches the strays, so a cancelled command's shell died and they ran
// on, reparented to init (ApplyGracefulCancel's "Process groups").
//
// # Every signal goes through a pidfd
//
// Each process in custody is held by a pidfd (pidfd_open), opened only once
// the process is proved the command's, and every signal is sent through it
// (pidfd_send_signal). A pidfd names one process for its whole life: once that
// process is gone a signal through it fails with ESRCH, and it can never reach
// whoever is given the pid next. A process group has no such handle: a signal
// to a group id names whichever group holds that number at delivery, and the
// id is free for reuse once its last member is gone, so no check made before
// the kill(-pgid) can prove the group is still the command's when the signal
// lands. So nothing here ever signals a group id; a group is only how members
// are found.
//
// # How a process is proved the command's
//
// Each reading lists the process table, then asks the pidfds in custody which
// of their processes are still alive. One alive after the listing held its pid
// throughout it, so what the listing says of that pid is its own:
//
//   - a child the listing gives a live process in custody is the command's;
//   - a group a live process in custody is listed in is still the command's
//     group (its id cannot be released while that member lives), so every
//     process listed in it is the command's too;
//   - at cancellation the command's process itself, unreaped, starts it.
//
// A process so found is held by a pidfd opened after the listing, and kept
// only when the process the pidfd names has the start time the listing gave
// and is alive after that was read: then the pidfd is the process the listing
// proved, not a later holder of its pid. Its children are walked only then.
//
// A group none of whose members in custody is alive any more is no longer
// provably the command's, and is dropped: what is in it now is not taken.
// Without a cgroup that is the residue: a member that forks and exits between
// two readings leaves a child nothing proves. The command's cgroup, where it
// has one, reaches that too (ApplyGracefulCancel's "Containment").
//
// # Signals
//
// SIGTERM goes to a stray group's LEADER while it lives, and to each of the
// group's members only once it is gone; SIGKILL, after grace, to every process
// in custody (systemd's KillMode=mixed). A process that made a group of its
// own did so to manage it, and signalling its members behind its back breaks
// that: `timeout` forwards a SIGTERM to its child and to its group itself, so
// a group-wide SIGTERM reached a bash child two or three times, and the later
// one, landing while bash ran its EXIT trap, killed it mid-trap (measured: 6
// of 12 traps lost, against none when only `timeout` was signalled). The
// command's own group gets its SIGTERM from terminateGracefully, while its
// leader, the daemon's `bash -c`, is unreaped. Processes taken into custody
// during grace get no SIGTERM, only the escalation's SIGKILL: nothing outlives
// the command's grace.
//
// Where pidfds are unavailable (Linux before 5.3, or a seccomp filter that
// refuses pidfd_open) nothing is taken into custody, and the escalation is
// killGroup's alone, as before descendants existed.
type descendants struct {
	// root is the command's process, the leader of its own group.
	root int
	// self is the daemon's own group, never one to take whole.
	self int
	// open holds a process by a reuse-safe handle (openPidfd); a test
	// substitutes its own.
	open func(GroupMember) (procRef, bool)
	// list reads the process table (processTable); a test substitutes its
	// own.
	list func() ([]tableEntry, bool)
	// tracking is false when nothing could be taken into custody (no pidfds,
	// no process table): the escalation is killGroup's then.
	tracking bool
	// held is every process in custody, by identity.
	held map[GroupMember]*heldProc
	// groups are the groups whose listed members are the command's: its own,
	// and the strays, each while a member in custody is alive in it.
	groups map[int]*trackedGroup
}

// procRef is a reuse-safe handle on one process: a signal through it reaches
// that process or nothing.
type procRef interface {
	signal(sig syscall.Signal) error
	// alive is whether the process is not yet reaped: while it is not, its
	// pid is still its own.
	alive() bool
	close()
}

// heldProc is one process in custody.
type heldProc struct {
	ref procRef
	// pgid is its group at the latest reading that listed it.
	pgid int
}

// trackedGroup is one process group whose listed members are the command's.
type trackedGroup struct {
	// leader is the process that made the group (its pid is the group id),
	// when it was in custody; nil when it was already gone.
	leader *GroupMember
	// own marks the command's own group, which terminateGracefully signals.
	own bool
}

// snapshotDescendants takes root, the command's process and group leader, and
// its descendants into custody. Call it while root is unreaped and before it
// is signalled: once root dies its children are reparented and are no longer
// reachable from it. Empty where pidfds or the process table are unavailable.
func snapshotDescendants(root int) *descendants {
	d := newDescendants(root, syscall.Getpgrp(), openPidfd, processTable)
	if root <= 0 || !pidfdsWork() {
		return d
	}
	table, ok := d.list()
	if !ok {
		return d
	}
	d.tracking = true
	d.groups[root] = &trackedGroup{own: true}
	d.read(table, true)
	return d
}

func newDescendants(root, self int, open func(GroupMember) (procRef, bool), list func() ([]tableEntry, bool)) *descendants {
	return &descendants{
		root:   root,
		self:   self,
		open:   open,
		list:   list,
		held:   make(map[GroupMember]*heldProc),
		groups: make(map[int]*trackedGroup),
	}
}

// refresh re-reads the process table and carries custody forward (read).
// Keeps the last reading where the table cannot be read.
func (d *descendants) refresh() {
	if len(d.held) == 0 {
		return
	}
	if table, ok := d.list(); ok {
		d.read(table, false)
	}
}

// read updates custody from one listing of the process table. initial is the
// reading at cancellation, which starts custody from the unreaped root.
func (d *descendants) read(table []tableEntry, initial bool) {
	byID := make(map[GroupMember]tableEntry, len(table))
	byPID := make(map[int]tableEntry, len(table))
	byGroup := make(map[int][]tableEntry)
	children := make(map[int][]tableEntry)
	for _, p := range table {
		byID[p.GroupMember] = p
		byPID[p.PID] = p
		byGroup[p.Pgid] = append(byGroup[p.Pgid], p)
		children[p.PPID] = append(children[p.PPID], p)
	}

	// Who in custody is still alive, asked AFTER the listing: what it says of
	// a live one is its own.
	for m, h := range d.held {
		if !h.ref.alive() {
			h.ref.close()
			delete(d.held, m)
			continue
		}
		if p, ok := byID[m]; ok {
			h.pgid = p.Pgid
		}
	}
	if initial {
		if p, ok := byPID[d.root]; ok {
			d.hold(p)
		}
	}

	// The groups still proved by a live member in custody.
	for pgid := range d.groups {
		if !d.provesGroup(pgid, byID) {
			delete(d.groups, pgid)
		}
	}

	// Everyone the listing proves the command's, breadth first: the members
	// of the proved groups and the live processes in custody, then their
	// children. Each is held before its children are walked, so a child is
	// taken only from a parent proved to be the one the listing names.
	visited := make(map[int]bool)
	var queue, found []tableEntry
	visit := func(p tableEntry, child bool) {
		if visited[p.PID] {
			return
		}
		visited[p.PID] = true
		if !d.hold(p) {
			return
		}
		queue = append(queue, p)
		if child {
			found = append(found, p)
		}
	}
	for m := range d.held {
		if p, ok := byID[m]; ok {
			visit(p, false)
		}
	}
	for pgid := range d.groups {
		for _, p := range byGroup[pgid] {
			visit(p, false)
		}
	}
	for ; len(queue) > 0; queue = queue[1:] {
		for _, c := range children[queue[0].PID] {
			visit(c, true)
		}
	}

	// A descendant outside every proved group is in a group of its own
	// making (a stray) when every process listed in that group was reached
	// from the command, and loose in someone else's otherwise: held alone,
	// its group never taken. Reached, not held: a member that exited before
	// it could be held (a loop's `sleep 0.05`) does not make the group
	// someone else's, and treating it so sent the stray's members a SIGTERM
	// of their own beside the one their leader forwards.
	for _, p := range found {
		if _, tracked := d.groups[p.Pgid]; tracked {
			continue
		}
		if p.Pgid <= 1 || p.Pgid == d.self || !allVisited(byGroup[p.Pgid], visited) {
			continue
		}
		g := &trackedGroup{}
		if l, ok := byPID[p.Pgid]; ok && l.Pgid == p.Pgid {
			leader := l.GroupMember
			g.leader = &leader
		}
		d.groups[p.Pgid] = g
	}
}

// hold takes p, proved the command's by the listing, into custody, and reports
// whether it is held: false when no handle on it could be opened, because it
// has gone or its pid now names another process.
func (d *descendants) hold(p tableEntry) bool {
	if h, ok := d.held[p.GroupMember]; ok {
		h.pgid = p.Pgid
		return true
	}
	ref, ok := d.open(p.GroupMember)
	if !ok {
		return false
	}
	d.held[p.GroupMember] = &heldProc{ref: ref, pgid: p.Pgid}
	return true
}

// provesGroup reports whether a live process in custody is listed in group
// pgid: its id cannot have been released while that member lives.
func (d *descendants) provesGroup(pgid int, byID map[GroupMember]tableEntry) bool {
	for m := range d.held {
		if p, ok := byID[m]; ok && p.Pgid == pgid {
			return true
		}
	}
	return false
}

// allVisited reports whether every process of members was reached.
func allVisited(members []tableEntry, visited map[int]bool) bool {
	for _, m := range members {
		if !visited[m.PID] {
			return false
		}
	}
	return true
}

// terminate sends SIGTERM to each stray group's leader while it is in custody,
// else to each of that group's members in custody, and to each loose process.
// The command's own group is terminateGracefully's.
func (d *descendants) terminate() {
	for m, h := range d.held {
		g, tracked := d.groups[h.pgid]
		switch {
		case !tracked:
			// Loose in someone else's group.
		case g.own:
			continue
		case g.leader != nil && d.held[*g.leader] != nil && *g.leader != m:
			// The leader manages its group; it alone is told.
			continue
		}
		_ = h.ref.signal(syscall.SIGTERM)
	}
}

// escalate is the escalation once grace has run out: it re-reads the tree and
// sends SIGKILL to every process in custody, each through its pidfd. Where
// nothing could be tracked it is killGroup's.
func (d *descendants) escalate(proc *os.Process, members []GroupMember) {
	if !d.tracking {
		killGroup(proc, members)
		return
	}
	d.killHeld()
	_ = proc.Kill()
}

// killHeld re-reads the tree and sends SIGKILL to every process in custody.
func (d *descendants) killHeld() {
	d.refresh()
	for _, h := range d.held {
		_ = h.ref.signal(syscall.SIGKILL)
	}
}

// alive reports whether anything is in custody as of the latest reading.
func (d *descendants) alive() bool {
	return len(d.held) > 0
}

// release closes every handle in custody.
func (d *descendants) release() {
	for m, h := range d.held {
		h.ref.close()
		delete(d.held, m)
	}
	clear(d.groups)
}

// pidfdRef holds a process by a pidfd.
type pidfdRef struct{ fd int }

func (r *pidfdRef) signal(sig syscall.Signal) error {
	return unix.PidfdSendSignal(r.fd, sig, nil, 0)
}

func (r *pidfdRef) alive() bool {
	// Signal 0 checks, delivering nothing. EPERM: alive, not ours to signal
	// (a setuid descendant); ESRCH: reaped.
	err := unix.PidfdSendSignal(r.fd, 0, nil, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

func (r *pidfdRef) close() { _ = unix.Close(r.fd) }

// openPidfd holds m by a pidfd. The pidfd names whoever had m's pid when it
// was opened, so it is kept only when that process has m's start time and is
// still alive after it was read: then the pid was its own throughout, and the
// start time read was its own.
func openPidfd(m GroupMember) (procRef, bool) {
	fd, err := unix.PidfdOpen(m.PID, 0)
	if err != nil {
		return nil, false
	}
	r := &pidfdRef{fd: fd}
	if _, _, _, start, ok := readProcStatParent(m.PID); !ok || start != m.Start || !r.alive() {
		r.close()
		return nil, false
	}
	return r, true
}

var (
	pidfdsOnce sync.Once
	pidfdsOK   bool
)

// pidfdsWork reports whether this kernel, and any seccomp filter around the
// daemon, allow pidfd_open and pidfd_send_signal.
func pidfdsWork() bool {
	pidfdsOnce.Do(func() {
		fd, err := unix.PidfdOpen(os.Getpid(), 0)
		if err != nil {
			return
		}
		defer func() { _ = unix.Close(fd) }()
		pidfdsOK = unix.PidfdSendSignal(fd, 0, nil, 0) == nil
	})
	return pidfdsOK
}
