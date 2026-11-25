// Copyright (c) 2026 Reliant Labs
//go:build !windows

package osutil

import (
	"slices"
	"testing"
)

// proc is one process of a fabricated process-table reading.
func proc(pid int, start uint64, ppid, pgid int) tableEntry {
	return tableEntry{GroupMember: GroupMember{PID: pid, Start: start}, PPID: ppid, Pgid: pgid}
}

// custodyOf starts custody of the command 100 (its own group 100) from the
// reading at cancellation, as snapshotDescendants does, with the daemon in
// group 1.
func custodyOf(initial ...tableEntry) *descendants {
	d := &descendants{
		root:   100,
		self:   1,
		groups: map[int]*trackedGroup{100: {own: true}},
		loose:  make(map[GroupMember]int),
	}
	d.read(initial, true)
	return d
}

func tracked(d *descendants) []int {
	var ids []int
	for pgid := range d.groups {
		ids = append(ids, pgid)
	}
	slices.Sort(ids)
	return ids
}

func custodyPIDs(d *descendants, pgid int) []int {
	g, ok := d.groups[pgid]
	if !ok {
		return nil
	}
	var pids []int
	for _, m := range g.custody {
		pids = append(pids, m.PID)
	}
	slices.Sort(pids)
	return pids
}

// TestDescendantsCustody pins the rule each re-read applies to carry a group
// forward ("Custody, not a snapshot"), on fabricated readings so every case is
// exact.
func TestDescendantsCustody(t *testing.T) {
	daemon := proc(1, 1, 0, 1)
	cmd := proc(100, 10, 1, 100)
	// timeout (200) made a group of its own; its child bash (201) is in it.
	timeout := proc(200, 20, 100, 200)
	work := proc(201, 21, 200, 200)

	t.Run("the strays at the cancel are taken into custody", func(t *testing.T) {
		d := custodyOf(daemon, cmd, timeout, work)
		if got := tracked(d); !slices.Equal(got, []int{100, 200}) {
			t.Fatalf("groups in custody = %v, want [100 200]", got)
		}
		if g := d.groups[200]; g.leader == nil || *g.leader != timeout.GroupMember {
			t.Fatalf("group 200's leader = %v, want timeout %v", g.leader, timeout.GroupMember)
		}
	})

	t.Run("a member alive carries the group's new members into custody", func(t *testing.T) {
		d := custodyOf(daemon, cmd, timeout, work)
		// The shell is gone; timeout too; bash forked a job (202).
		d.read([]tableEntry{daemon, work, proc(202, 30, 201, 200)}, false)
		if got := custodyPIDs(d, 200); !slices.Equal(got, []int{201, 202}) {
			t.Fatalf("group 200's custody = %v, want [201 202]", got)
		}
		// Then bash exits; the job it forked still proves the group.
		d.read([]tableEntry{daemon, proc(202, 30, 1, 200)}, false)
		if got := custodyPIDs(d, 200); !slices.Equal(got, []int{202}) {
			t.Fatalf("group 200's custody = %v, want [202]", got)
		}
		if _, ok := d.groups[100]; ok {
			t.Fatal("the command's own group, emptied, is still in custody")
		}
	})

	t.Run("a member that forks and exits between readings leaves its group in custody", func(t *testing.T) {
		d := custodyOf(daemon, cmd, timeout, work)
		// Every member of the previous reading is gone; 202 was born since.
		d.read([]tableEntry{daemon, proc(202, 30, 1, 200)}, false)
		if got := custodyPIDs(d, 200); !slices.Equal(got, []int{202}) {
			t.Fatalf("group 200's custody = %v, want [202]: a job forked between readings was lost", got)
		}
	})

	t.Run("a reading that misses a member's exit and its child's birth keeps the group", func(t *testing.T) {
		d := custodyOf(daemon, cmd, timeout, work)
		// /proc was listed, then 201 forked 202 and both 200 and 201 exited
		// before they were read: the reading finds group 200 empty.
		d.read([]tableEntry{daemon}, false)
		if _, ok := d.groups[200]; !ok || !d.alive() {
			t.Fatal("group 200 dropped on one empty reading")
		}
		d.read([]tableEntry{daemon, proc(202, 30, 1, 200)}, false)
		if got := custodyPIDs(d, 200); !slices.Equal(got, []int{202}) {
			t.Fatalf("group 200's custody = %v, want [202]", got)
		}
	})

	t.Run("two empty readings drop the group", func(t *testing.T) {
		d := custodyOf(daemon, cmd, timeout, work)
		d.read([]tableEntry{daemon}, false)
		d.read([]tableEntry{daemon}, false)
		if len(d.groups) != 0 || d.alive() {
			t.Fatalf("groups in custody after two empty readings = %v, want none", tracked(d))
		}
	})

	t.Run("a process seen before that joins the emptied group does not prove it", func(t *testing.T) {
		other := proc(50, 5, 1, 50)
		d := custodyOf(daemon, other, cmd, timeout, work)
		d.read([]tableEntry{daemon, proc(50, 5, 1, 200)}, false)
		if _, ok := d.groups[200]; ok {
			t.Fatal("group 200 kept in custody on a process that was not born into it")
		}
	})

	t.Run("the group id's number held by another process drops the group", func(t *testing.T) {
		d := custodyOf(daemon, cmd, timeout, work)
		// 200 was released and handed out again: a stranger's new group,
		// with a child born since the previous reading.
		d.read([]tableEntry{daemon, proc(200, 99, 1, 200), proc(203, 99, 200, 200)}, false)
		if _, ok := d.groups[200]; ok {
			t.Fatal("group 200 kept in custody though its number belongs to another process")
		}
	})

	t.Run("a group a descendant makes during grace is taken into custody", func(t *testing.T) {
		d := custodyOf(daemon, cmd, timeout, work)
		// bash's cleanup starts a server with setsid (300, group 300).
		d.read([]tableEntry{daemon, work, proc(300, 40, 201, 300)}, false)
		if got := custodyPIDs(d, 300); !slices.Equal(got, []int{300}) {
			t.Fatalf("group 300's custody = %v, want [300]", got)
		}
	})

	t.Run("a descendant in a group shared with a stranger stays loose", func(t *testing.T) {
		stranger := proc(400, 4, 1, 400)
		joined := proc(401, 41, 100, 400)
		d := custodyOf(daemon, stranger, cmd, joined)
		if _, ok := d.groups[400]; ok {
			t.Fatal("a group shared with a stranger was taken into custody whole")
		}
		if pgid, ok := d.loose[joined.GroupMember]; !ok || pgid != 400 {
			t.Fatalf("loose = %v, want %v in group 400", d.loose, joined.GroupMember)
		}
	})
}
