// Copyright (c) 2026 Reliant Labs
//go:build !windows

package osutil

import (
	"syscall"
)

// descendants is the process tree of a cancelled command, kept in custody
// through the grace period so the escalation reaches all of it.
//
// It holds the command's own process group and the groups its descendants made
// of their own (strays): `timeout` takes one unless given --foreground, and
// setsid makes one. A signal to the command's group never reaches those, so a
// cancelled command's shell died and they ran on, reparented to init
// (ApplyGracefulCancel's "Process groups").
//
// # Custody, not a snapshot
//
// The tree is read at cancellation and re-read every custodyPoll until the
// escalation. A group's member list read once goes stale: during grace its
// members fork (a trap's cleanup, the next step of a script) and exit, and the
// escalation proved the group still the command's only by a member of that one
// reading. Once those were all gone it skipped the SIGKILL, and whatever they
// had forked ran on. So each re-read carries the proof forward: a group is
// still the command's when a member of the previous reading is alive in it,
// unchanged (the kernel does not hand out a pid still in use as a group id, so
// the id was never released), and then everyone in it now is the command's
// too. A member that forks and exits between two readings leaves only
// processes born since the previous one: those are the group's as well, since
// for the id to have been released and taken by a stranger's new group, the
// pid space would have had to wrap around to it within one interval, and the
// process given its number (which would hold it) be gone again already. Any
// process now holding the id's number that is not the recorded leader proves
// the id was released, and the group is dropped unsignalled.
//
// A reading is not an instant: /proc is listed, then each process read. A
// member that exits after the listing, having forked after it, is missing from
// that reading and so is its child, and the group reads empty though it never
// was. So an empty reading drops a group only when the one before it was empty
// too; until then the group stays in custody, and the next reading finds the
// child, born since the empty one.
//
// Each re-read also walks the custody's children, so a group a descendant
// makes during grace (a cleanup that starts something with setsid) is taken
// into custody too. One made by a process that then exits before the next
// reading is reparented beyond reach: that is the residue polling leaves.
//
// # Signals
//
// SIGTERM goes to a stray group's LEADER while it lives, and to the whole
// group only once it is gone; SIGKILL, after grace, always to the whole group
// (systemd's KillMode=mixed). A process that made a group of its own did so to
// manage it, and signalling its members behind its back breaks that: `timeout`
// forwards a SIGTERM to its child and to its group itself, so a group-wide
// SIGTERM reached a bash child two or three times, and the later one, landing
// while bash ran its EXIT trap, killed it mid-trap (measured: 6 of 12 traps
// lost, against none when only `timeout` was signalled). The command's own
// group is signalled whole by terminateGracefully: its leader is the daemon's
// `bash -c`, which forwards nothing. Groups taken into custody during grace
// get no SIGTERM, only the escalation's SIGKILL: nothing outlives the
// command's grace.
type descendants struct {
	// root is the command's process, the leader of its own group.
	root int
	// self is the daemon's own group, never one to signal whole.
	self int
	// groups are the groups in custody by id: the command's own, and those
	// whose every member is the command's.
	groups map[int]*trackedGroup
	// loose are descendants sharing a group with a process that is not the
	// command's, by identity, with the group they were in: signalled one by
	// one, never the group, which is not the command's to stop.
	loose map[GroupMember]int
	// seen is every process of the previous reading, to tell a process born
	// since then.
	seen map[GroupMember]bool
}

// trackedGroup is one process group in custody.
type trackedGroup struct {
	// custody is the group's members at the latest reading that proved the
	// group still the command's. Empty when that reading found none: the
	// group is kept for one more ("A reading is not an instant").
	custody []GroupMember
	// leader is the process that made the group (its pid is the group id),
	// when it was seen alive; nil when it was already gone.
	leader *GroupMember
	// own marks the command's own group, which terminateGracefully signals.
	own bool
}

// snapshotDescendants takes root, the command's process and group leader, and
// its descendants into custody. Call it while root is unreaped and before it
// is signalled: the unreaped leader proves the group id is still root's, and
// once root dies its children are reparented and are no longer reachable from
// it. Empty where the process table cannot be read.
func snapshotDescendants(root int) *descendants {
	d := &descendants{
		root:   root,
		self:   syscall.Getpgrp(),
		groups: make(map[int]*trackedGroup),
		loose:  make(map[GroupMember]int),
	}
	if root <= 0 {
		return d
	}
	table, ok := processTable()
	if !ok {
		return d
	}
	d.groups[root] = &trackedGroup{own: true}
	d.read(table, true)
	return d
}

// refresh re-reads the process table and carries custody forward (read).
// Keeps the last reading where the table cannot be read.
func (d *descendants) refresh() {
	if len(d.groups) == 0 && len(d.loose) == 0 {
		return
	}
	if table, ok := processTable(); ok {
		d.read(table, false)
	}
}

// read updates custody from one reading of the process table. initial is the
// reading at cancellation, when the command's own group is proved by its
// unreaped leader rather than by an earlier reading.
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

	// The groups in custody: still the command's, with everyone in them now.
	for pgid, g := range d.groups {
		cur := byGroup[pgid]
		if !initial && !d.stillOwned(pgid, g, cur, byID, byPID) {
			delete(d.groups, pgid)
			continue
		}
		g.custody = g.custody[:0]
		for _, p := range cur {
			g.custody = append(g.custody, p.GroupMember)
			if p.PID == pgid && g.leader == nil && initial {
				leader := p.GroupMember
				g.leader = &leader
			}
		}
	}
	for m, pgid := range d.loose {
		if p, ok := byID[m]; !ok || p.Pgid != pgid {
			delete(d.loose, m)
		}
	}

	// Everyone in custody, and their descendants by breadth-first walk; ours
	// guards a cycle in a table read while it changed.
	ours := make(map[int]bool)
	var queue []int
	for _, g := range d.groups {
		for _, m := range g.custody {
			ours[m.PID] = true
			queue = append(queue, m.PID)
		}
	}
	for m := range d.loose {
		ours[m.PID] = true
		queue = append(queue, m.PID)
	}
	var found []tableEntry
	for ; len(queue) > 0; queue = queue[1:] {
		for _, c := range children[queue[0]] {
			if ours[c.PID] {
				continue
			}
			ours[c.PID] = true
			queue = append(queue, c.PID)
			found = append(found, c)
		}
	}

	// A descendant outside every group in custody is in a group of its own
	// making (a stray), or loose in someone else's.
	for _, p := range found {
		if _, tracked := d.groups[p.Pgid]; tracked {
			continue
		}
		if p.Pgid <= 1 || p.Pgid == d.self || !allOurs(byGroup[p.Pgid], ours) {
			d.loose[p.GroupMember] = p.Pgid
			continue
		}
		g := &trackedGroup{}
		for _, m := range byGroup[p.Pgid] {
			g.custody = append(g.custody, m.GroupMember)
			if m.PID == p.Pgid {
				leader := m.GroupMember
				g.leader = &leader
			}
		}
		d.groups[p.Pgid] = g
	}

	d.seen = make(map[GroupMember]bool, len(table))
	for _, p := range table {
		d.seen[p.GroupMember] = true
	}
}

// stillOwned reports whether group pgid, in custody at the previous reading,
// is still the command's in this one, whose members of it are cur (the proof
// is descendants' "Custody, not a snapshot").
func (d *descendants) stillOwned(pgid int, g *trackedGroup, cur []tableEntry, byID map[GroupMember]tableEntry, byPID map[int]tableEntry) bool {
	// A process holding the id's number that is not the leader recorded for
	// it: the number was released, so the group emptied.
	if p, ok := byPID[pgid]; ok && (g.leader == nil || p.GroupMember != *g.leader) {
		return false
	}
	for _, m := range g.custody {
		if p, ok := byID[m]; ok && p.Pgid == pgid {
			return true
		}
	}
	// Every member of the previous reading is gone. Those in it now are still
	// the group's if each was born since that reading. None: kept for one
	// more reading, unless the previous one found none either.
	if len(cur) == 0 {
		return len(g.custody) > 0
	}
	for _, p := range cur {
		if d.seen[p.GroupMember] {
			return false
		}
	}
	return true
}

// allOurs reports whether every process of members is in ours.
func allOurs(members []tableEntry, ours map[int]bool) bool {
	for _, m := range members {
		if !ours[m.PID] {
			return false
		}
	}
	return true
}

// terminate sends SIGTERM to each stray group's leader while it is still the
// same process, else to the group while it is still the command's, and to each
// loose process. The command's own group is terminateGracefully's.
func (d *descendants) terminate() {
	for pgid, g := range d.groups {
		switch {
		case g.own:
		case g.leader != nil && GroupOwned(pgid, []GroupMember{*g.leader}):
			_ = syscall.Kill(g.leader.PID, syscall.SIGTERM)
		case GroupOwned(pgid, g.custody):
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}
	}
	d.signalLoose(syscall.SIGTERM)
}

// kill re-reads the tree and sends SIGKILL to every group in custody and every
// loose process still the command's. A group the reading found empty is read
// again first, so what was born during that reading is found or the group is
// dropped (the bound only stops a group that empties on every reading).
func (d *descendants) kill() {
	d.refresh()
	for range 3 {
		if !d.anyEmpty() {
			break
		}
		d.refresh()
	}
	for pgid := range d.groups {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	d.signalLoose(syscall.SIGKILL)
}

// anyEmpty reports whether a group in custody had no members at the latest
// reading.
func (d *descendants) anyEmpty() bool {
	for _, g := range d.groups {
		if len(g.custody) == 0 {
			return true
		}
	}
	return false
}

// signalLoose sends sig to each loose process while it is still the same
// process in the same group.
func (d *descendants) signalLoose(sig syscall.Signal) {
	for m, pgid := range d.loose {
		if GroupOwned(pgid, []GroupMember{m}) {
			_ = syscall.Kill(m.PID, sig)
		}
	}
}

// alive reports whether anything is in custody as of the latest reading,
// including a group it found empty and keeps for one more. It answers from
// that reading, not from a fresh check of the members: one that exits just
// after it may have forked first, and calling the escalation off on its
// absence lost what it forked. The next reading finds that.
func (d *descendants) alive() bool {
	return len(d.groups) > 0 || len(d.loose) > 0
}
