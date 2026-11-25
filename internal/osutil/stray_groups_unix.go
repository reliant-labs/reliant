// Copyright (c) 2026 Reliant Labs
//go:build !windows

package osutil

import (
	"slices"
	"syscall"
)

// strayGroups are the process groups a command's descendants made of their
// own, apart from the command's group: `timeout` takes one unless given
// --foreground, and setsid makes one. A signal to the command's group never
// reaches them, so a cancelled command's shell died and they ran on,
// reparented to init (ApplyGracefulCancel's "Process groups").
//
// Each is recorded with its members, so a later signal can prove the id is
// still the one the command's descendant made (GroupOwned), exactly as the
// command's own group is proved before its escalation.
//
// SIGTERM goes to a stray group's LEADER while it lives, and to the whole
// group only once it is gone; SIGKILL, after grace, always to the whole group
// (systemd's KillMode=mixed). A process that made a group of its own did so to
// manage it, and signalling its members behind its back breaks that: `timeout`
// forwards a SIGTERM to its child and to its group itself, so a group-wide
// SIGTERM reached a bash child two or three times, and the later one, landing
// while bash ran its EXIT trap, killed it mid-trap (measured: 6 of 12 traps
// lost, against none when only `timeout` was signalled). The command's own
// group is still signalled whole: its leader is the daemon's `bash -c`, which
// forwards nothing.
type strayGroups struct {
	// groups are those whose every member is a descendant of the command,
	// which also reaches members started after the snapshot.
	groups map[int]strayGroup
	// loose are descendants sharing a group with a process that is not the
	// command's: signalled one by one, never the group, which is not the
	// command's to stop.
	loose []looseProcess
}

// strayGroup is one recorded stray group.
type strayGroup struct {
	members []GroupMember
	// leader is the member that made the group (its pid is the group id),
	// when it is a live descendant; nil when it is gone.
	leader *GroupMember
}

// looseProcess is a descendant signalled alone, with the group it was in.
type looseProcess struct {
	pgid int
	m    GroupMember
}

// snapshotStrayGroups reads the descendants of leader, the command's process
// and group leader, and records the groups they are in other than leader's
// own and the daemon's. Call it while leader is unreaped and before it is
// signalled: once it dies its children are reparented and are no longer
// reachable from it. Empty where the process table cannot be read.
func snapshotStrayGroups(leader int) strayGroups {
	var s strayGroups
	if leader <= 0 {
		return s
	}
	table, ok := processTable()
	if !ok {
		return s
	}
	children := make(map[int][]tableEntry, len(table))
	byGroup := make(map[int][]tableEntry)
	for _, p := range table {
		children[p.PPID] = append(children[p.PPID], p)
		byGroup[p.Pgid] = append(byGroup[p.Pgid], p)
	}

	// The descendants, by breadth-first walk from the leader. seen guards a
	// cycle in a table read while it changed.
	descendant := map[int]bool{leader: true}
	var stray []tableEntry
	for queue := []int{leader}; len(queue) > 0; queue = queue[1:] {
		for _, c := range children[queue[0]] {
			if descendant[c.PID] {
				continue
			}
			descendant[c.PID] = true
			queue = append(queue, c.PID)
			if c.Pgid != leader {
				stray = append(stray, c)
			}
		}
	}

	self := syscall.Getpgrp()
	for _, p := range stray {
		if p.Pgid <= 1 || p.Pgid == self {
			s.loose = append(s.loose, looseProcess{pgid: p.Pgid, m: p.GroupMember})
			continue
		}
		if _, done := s.groups[p.Pgid]; done {
			continue
		}
		members := byGroup[p.Pgid]
		if !slices.ContainsFunc(members, func(m tableEntry) bool { return !descendant[m.PID] }) {
			if s.groups == nil {
				s.groups = make(map[int]strayGroup)
			}
			var g strayGroup
			for _, m := range members {
				g.members = append(g.members, m.GroupMember)
				if m.PID == p.Pgid {
					leader := m.GroupMember
					g.leader = &leader
				}
			}
			s.groups[p.Pgid] = g
			continue
		}
		s.loose = append(s.loose, looseProcess{pgid: p.Pgid, m: p.GroupMember})
	}
	return s
}

// terminate sends SIGTERM to each stray group's leader while it is still the
// same process, else to the group while it is still the command's, and to each
// loose process.
func (s strayGroups) terminate() {
	for pgid, g := range s.groups {
		if g.leader != nil && GroupOwned(pgid, []GroupMember{*g.leader}) {
			_ = syscall.Kill(g.leader.PID, syscall.SIGTERM)
		} else if GroupOwned(pgid, g.members) {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}
	}
	s.signalLoose(syscall.SIGTERM)
}

// kill sends SIGKILL to every stray group and loose process still the
// command's.
func (s strayGroups) kill() {
	for pgid, g := range s.groups {
		if GroupOwned(pgid, g.members) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
	s.signalLoose(syscall.SIGKILL)
}

// signalLoose sends sig to each loose process while it is still the same
// process in the same group.
func (s strayGroups) signalLoose(sig syscall.Signal) {
	for _, l := range s.loose {
		if GroupOwned(l.pgid, []GroupMember{l.m}) {
			_ = syscall.Kill(l.m.PID, sig)
		}
	}
}

// alive reports whether any stray group or loose process is still running.
func (s strayGroups) alive() bool {
	for pgid, g := range s.groups {
		if GroupOwned(pgid, g.members) {
			return true
		}
	}
	for _, l := range s.loose {
		if GroupOwned(l.pgid, []GroupMember{l.m}) {
			return true
		}
	}
	return false
}
