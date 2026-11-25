// Copyright (c) 2026 Reliant Labs
//go:build linux

package osutil

import (
	"slices"
	"syscall"
	"testing"
)

// proc is one process of a fabricated process-table reading.
func proc(pid int, start uint64, ppid, pgid int) tableEntry {
	return tableEntry{GroupMember: GroupMember{PID: pid, Start: start}, PPID: ppid, Pgid: pgid}
}

// world stands in for the kernel behind fabricated readings: which processes
// are alive (not reaped), and every signal sent through a handle on one.
type world struct {
	alive   map[GroupMember]bool
	opened  []GroupMember
	signals []sent
}

type sent struct {
	to  GroupMember
	sig syscall.Signal
}

type fakeRef struct {
	w *world
	m GroupMember
}

func (r *fakeRef) signal(sig syscall.Signal) error {
	if !r.w.alive[r.m] {
		return syscall.ESRCH
	}
	r.w.signals = append(r.w.signals, sent{r.m, sig})
	return nil
}
func (r *fakeRef) alive() bool { return r.w.alive[r.m] }
func (r *fakeRef) close()      {}

// open holds m only while it is alive, as openPidfd does.
func (w *world) open(m GroupMember) (procRef, bool) {
	if !w.alive[m] {
		return nil, false
	}
	w.opened = append(w.opened, m)
	return &fakeRef{w: w, m: m}, true
}

// reading makes every process of table alive, and everything else not: what
// a reading at that instant shows.
func (w *world) reading(table ...tableEntry) []tableEntry {
	w.alive = make(map[GroupMember]bool, len(table))
	for _, p := range table {
		w.alive[p.GroupMember] = true
	}
	return table
}

// custodyOf starts custody of the command 100 (its own group 100) from the
// reading at cancellation, as snapshotDescendants does, with the daemon in
// group 1.
func custodyOf(w *world, initial ...tableEntry) *descendants {
	d := newDescendants(100, 1, w.open, nil)
	d.tracking = true
	d.groups[100] = &trackedGroup{own: true}
	d.read(w.reading(initial...), true)
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

func heldPIDs(d *descendants) []int {
	var pids []int
	for m := range d.held {
		pids = append(pids, m.PID)
	}
	slices.Sort(pids)
	return pids
}

func signalled(w *world, sig syscall.Signal) []int {
	var pids []int
	for _, s := range w.signals {
		if s.sig == sig {
			pids = append(pids, s.to.PID)
		}
	}
	slices.Sort(pids)
	return pids
}

func TestDescendantsCustody(t *testing.T) {
	daemon := proc(1, 1, 0, 1)
	cmd := proc(100, 10, 1, 100)
	// timeout (200) made a group of its own; its child bash (201) is in it.
	timeout := proc(200, 20, 100, 200)
	work := proc(201, 21, 200, 200)

	t.Run("the strays at the cancel are taken into custody", func(t *testing.T) {
		w := &world{}
		d := custodyOf(w, daemon, cmd, timeout, work)
		if got := tracked(d); !slices.Equal(got, []int{100, 200}) {
			t.Fatalf("groups in custody = %v, want [100 200]", got)
		}
		if got := heldPIDs(d); !slices.Equal(got, []int{100, 200, 201}) {
			t.Fatalf("held = %v, want [100 200 201]", got)
		}
		if g := d.groups[200]; g.leader == nil || *g.leader != timeout.GroupMember {
			t.Fatalf("group 200's leader = %v, want timeout %v", g.leader, timeout.GroupMember)
		}
	})

	t.Run("SIGTERM goes to a stray's leader alone, and to its members once it is gone", func(t *testing.T) {
		w := &world{}
		d := custodyOf(w, daemon, cmd, timeout, work)
		d.terminate()
		if got := signalled(w, syscall.SIGTERM); !slices.Equal(got, []int{200}) {
			t.Fatalf("SIGTERM to %v, want only timeout [200] (the command's own group is terminateGracefully's)", got)
		}

		w = &world{}
		d = custodyOf(w, daemon, cmd, proc(201, 21, 100, 200), proc(202, 22, 201, 200))
		d.terminate()
		if got := signalled(w, syscall.SIGTERM); !slices.Equal(got, []int{201, 202}) {
			t.Fatalf("SIGTERM to %v, want each member of the leaderless group [201 202]", got)
		}
	})

	t.Run("a live member carries the group's new members into custody", func(t *testing.T) {
		w := &world{}
		d := custodyOf(w, daemon, cmd, timeout, work)
		// The shell is gone; timeout too; bash forked a job (202).
		d.read(w.reading(daemon, work, proc(202, 30, 201, 200)), false)
		if got := heldPIDs(d); !slices.Equal(got, []int{201, 202}) {
			t.Fatalf("held = %v, want [201 202]", got)
		}
		// Then bash exits; the job it forked, held, still proves the group,
		// and what the job forks is taken too.
		d.read(w.reading(daemon, proc(202, 30, 1, 200), proc(203, 31, 202, 200)), false)
		if got := heldPIDs(d); !slices.Equal(got, []int{202, 203}) {
			t.Fatalf("held = %v, want [202 203]", got)
		}
		if got := tracked(d); !slices.Equal(got, []int{200}) {
			t.Fatalf("groups in custody = %v, want [200]: the command's own group emptied", got)
		}
	})

	t.Run("the escalation SIGKILLs every process in custody through its handle", func(t *testing.T) {
		w := &world{}
		d := custodyOf(w, daemon, cmd, timeout, work)
		// At grace the shell and timeout are gone, and bash forked a job.
		d.list = func() ([]tableEntry, bool) { return w.reading(daemon, work, proc(202, 30, 201, 200)), true }
		d.killHeld()
		if got := signalled(w, syscall.SIGKILL); !slices.Equal(got, []int{201, 202}) {
			t.Fatalf("SIGKILL to %v, want [201 202]", got)
		}
	})

	t.Run("a group a descendant makes during grace is taken into custody", func(t *testing.T) {
		w := &world{}
		d := custodyOf(w, daemon, cmd, timeout, work)
		// bash's cleanup starts a server with setsid (300, group 300).
		d.read(w.reading(daemon, work, proc(300, 40, 201, 300)), false)
		if _, ok := d.groups[300]; !ok || d.held[proc(300, 40, 201, 300).GroupMember] == nil {
			t.Fatalf("group 300 not in custody: groups %v, held %v", tracked(d), heldPIDs(d))
		}
	})

	t.Run("a descendant in a group shared with a stranger is held alone", func(t *testing.T) {
		w := &world{}
		stranger := proc(400, 4, 1, 400)
		joined := proc(401, 41, 100, 400)
		d := custodyOf(w, daemon, stranger, cmd, joined)
		if _, ok := d.groups[400]; ok {
			t.Fatal("a group shared with a stranger was taken into custody whole")
		}
		if got := heldPIDs(d); !slices.Equal(got, []int{100, 401}) {
			t.Fatalf("held = %v, want [100 401]: the stranger must never be held", got)
		}
		d.terminate()
		if got := signalled(w, syscall.SIGTERM); !slices.Equal(got, []int{401}) {
			t.Fatalf("SIGTERM to %v, want only the loose descendant [401]", got)
		}
	})

	t.Run("a member gone before it could be held leaves its group a stray", func(t *testing.T) {
		w := &world{}
		// The work's `sleep 0.05` (202) is listed, then exits before a
		// handle on it is opened.
		table := w.reading(daemon, cmd, timeout, work, proc(202, 22, 201, 200))
		delete(w.alive, proc(202, 22, 201, 200).GroupMember)
		d := newDescendants(100, 1, w.open, nil)
		d.tracking = true
		d.groups[100] = &trackedGroup{own: true}
		d.read(table, true)
		if _, ok := d.groups[200]; !ok {
			t.Fatalf("groups in custody = %v, want 200 a stray", tracked(d))
		}
		d.terminate()
		if got := signalled(w, syscall.SIGTERM); !slices.Equal(got, []int{200}) {
			t.Fatalf("SIGTERM to %v, want only timeout [200]", got)
		}
	})

	t.Run("a process whose pid now names another is not held", func(t *testing.T) {
		w := &world{}
		d := custodyOf(w, daemon, cmd, timeout, work)
		// The listing shows 202 in group 200, but by the time a handle is
		// opened its pid holds a process with another start time.
		table := w.reading(daemon, work, proc(202, 30, 201, 200))
		delete(w.alive, proc(202, 30, 201, 200).GroupMember)
		w.alive[proc(202, 99, 1, 202).GroupMember] = true
		d.read(table, false)
		if got := heldPIDs(d); !slices.Equal(got, []int{201}) {
			t.Fatalf("held = %v, want [201]", got)
		}
	})
}

// TestDescendantsNeverSignalAGroupTheyCannotProve: once every member of a
// stray group in custody is gone, its id may be released and taken by an
// unrelated group, so nothing listed under it is taken or signalled. The
// stale proof the escalation used to SIGKILL -pgid on (reliant#3542, review
// B1) is never enough.
func TestDescendantsNeverSignalAGroupTheyCannotProve(t *testing.T) {
	daemon := proc(1, 1, 0, 1)
	cmd := proc(100, 10, 1, 100)
	leader := proc(200, 20, 100, 200)
	member := proc(201, 21, 200, 200)

	for name, later := range map[string][]tableEntry{
		// 200 released and handed to a stranger, whose new group took the id.
		"the id taken by a stranger's group": {daemon, proc(200, 90, 1, 200), proc(203, 91, 200, 200)},
		// A process born since, not the command's, joins the number.
		"a stranger listed under the id": {daemon, proc(500, 92, 1, 200)},
	} {
		t.Run(name, func(t *testing.T) {
			w := &world{}
			d := custodyOf(w, daemon, cmd, leader, member)
			opened := len(w.opened)
			// The whole stray group and the command are gone by grace.
			d.list = func() ([]tableEntry, bool) { return w.reading(later...), true }
			d.terminate()
			w.signals = nil
			d.killHeld()
			if _, ok := d.groups[200]; ok {
				t.Fatal("group 200 kept in custody with no live member in custody to prove it")
			}
			if len(w.opened) != opened || d.alive() {
				t.Fatalf("opened %v, held %v after the group's members were gone: a stranger was taken into custody", w.opened[opened:], heldPIDs(d))
			}
			if len(w.signals) != 0 {
				t.Fatalf("signals sent: %v; want none to a group nothing in custody proves", w.signals)
			}
		})
	}
}
