//go:build linux

package worktreereclaim

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// blockerPID is a pid no other test, and no earlier run of this one in the same
// process (-count, -cpu), has reported: the blocker record is process-wide.
func blockerPID() int { return 4_900_200 + rand.IntN(90_000) }

func blockerFor(pid int) (ReclaimBlocker, bool) {
	for _, b := range Blockers() {
		if b.PID == pid {
			return b, true
		}
	}
	return ReclaimBlocker{}, false
}

// An uninspectable process of the user still holds (fail-closed), and the hold
// names it: one blocker per process, counting each workspace it held once.
func TestUninspectable_HoldNamesItsProcessOncePerWorkspace(t *testing.T) {
	pid := blockerPID()
	proc := uninspectableFixture(t, pid, "slot-holder", "slot-holder", argv("/usr/bin/python3", "-I"), pid, 777)
	a, b := t.TempDir(), t.TempDir()
	for _, roots := range [][]string{{a}, {b, filepath.Join(b, ".reclaim", "id")}, {a}} {
		err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), roots)
		var u *UninspectableProcessError
		if !errors.As(err, &u) {
			t.Fatalf("an uninspectable process must hold, naming itself: %v", err)
		}
		if u.PID != pid || u.Comm != "slot-holder" || u.What != "maps" || u.StartTicks != 777 {
			t.Fatalf("hold does not identify the process: %+v", u)
		}
		if !strings.Contains(err.Error(), "process "+strconv.Itoa(pid)+" (slot-holder) maps unreadable") {
			t.Fatalf("hold message changed: %v", err)
		}
	}
	got, ok := blockerFor(pid)
	if !ok {
		t.Fatalf("no blocker reported; have %+v", Blockers())
	}
	if got.Worktrees != 2 || got.Comm != "slot-holder" || got.Unreadable != "maps" {
		t.Fatalf("blocker not aggregated per process and workspace: %+v", got)
	}
	line := got.String()
	for _, want := range []string{"pid " + strconv.Itoa(pid) + " (slot-holder, up ", "blocks worktree reclaim: 2 worktree(s) held", "its maps cannot be read"} {
		if !strings.Contains(line, want) {
			t.Errorf("blocker line %q lacks %q", line, want)
		}
	}
}

// What does not hold, or holds for another reason, is not a blocker.
func TestUninspectable_OnlyUninspectableHoldsAreBlockers(t *testing.T) {
	t.Run("a known agent", func(t *testing.T) {
		pid := blockerPID()
		proc := uninspectableFixture(t, pid, "ssh-agent", "ssh-agent", argv("ssh-agent", "-s"), pid, 777)
		if err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if _, ok := blockerFor(pid); ok {
			t.Fatal("a known agent is not a blocker")
		}
	})
	t.Run("a state missing for another reason", func(t *testing.T) {
		pid := blockerPID()
		proc := uninspectableFixture(t, pid, "slot-holder", "slot-holder", argv("x"), pid, 777)
		if err := os.Remove(filepath.Join(proc, strconv.Itoa(pid), "maps")); err != nil {
			t.Fatal(err)
		}
		err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{t.TempDir()})
		var u *UninspectableProcessError
		if err == nil || errors.As(err, &u) {
			t.Fatalf("a missing file still holds, but is not uninspectability: %v", err)
		}
		if _, ok := blockerFor(pid); ok {
			t.Fatal("not a blocker")
		}
	})
}

// A blocker no check has met for an hour is no longer reported: the process
// ended, or nothing asked.
func TestBlockersExpire(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	s.note(&UninspectableProcessError{PID: 7, Comm: "x", What: "cwd", Err: os.ErrPermission}, []string{"/w"})
	if got := s.list(); len(got) != 1 || got[0].Worktrees != 1 {
		t.Fatalf("got %+v", got)
	}
	now = now.Add(blockerTTL + time.Minute)
	if got := s.list(); len(got) != 0 {
		t.Fatalf("expired blocker still reported: %+v", got)
	}
}

// identified is an observation of pid with a readable stat start time and no
// boot time: Started (display only) stays zero.
func identified(pid int, ticks uint64, what string) *UninspectableProcessError {
	return &UninspectableProcessError{PID: pid, Comm: "x", What: what, StartTicks: ticks, Err: os.ErrPermission}
}

func unidentifiedObs(pid int) *UninspectableProcessError {
	return &UninspectableProcessError{PID: pid, What: "cwd", Err: os.ErrPermission}
}

func split(bs []ReclaimBlocker) (ids, unids []ReclaimBlocker) {
	for _, b := range bs {
		if b.Unidentified {
			unids = append(unids, b)
		} else {
			ids = append(ids, b)
		}
	}
	return ids, unids
}

// Identity is (pid, stat start ticks), with or without the boot time: a live
// process met on every sweep is one blocker even when /proc/stat has no btime,
// and the same pid with other start ticks (a reused pid) is another process.
func TestBlockersIdentifyByStartTicksWithoutBootTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	for range 200 { // 200 sweeps, 10 minutes apart, 3 worktrees each
		now = now.Add(10 * time.Minute)
		for w := range 3 {
			s.note(identified(9, 777, "cwd"), []string{"/w" + strconv.Itoa(w)})
		}
	}
	s.note(identified(9, 999, "fd 3"), []string{"/w0"}) // pid 9 reused
	ids, unids := split(s.list())
	if len(unids) != 0 || len(ids) != 2 {
		t.Fatalf("want two identified processes, got identified=%+v unidentified=%+v", ids, unids)
	}
	if ids[0].Worktrees != 3 || ids[1].Worktrees != 1 || ids[1].Unreadable != "fd 3" {
		t.Fatalf("a reused pid merged into, or split from, the live process: %+v", ids)
	}
	if strings.Contains(ids[0].String(), "up ") {
		t.Fatalf("no boot time, so no age to show: %s", ids[0])
	}
}

// With no start time at all, a pid's observations aggregate only while they
// keep coming within unidentifiedWindow; a gap is a new entry (the pid may
// have been reused), and pids never merge with each other.
func TestBlockersUnidentifiedCollapseOnlyWithinTheWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	for i := range 100 { // one live process met every 30s, on 2 worktrees
		now = now.Add(30 * time.Second)
		s.note(unidentifiedObs(9), []string{"/w" + strconv.Itoa(i%2)})
	}
	s.note(unidentifiedObs(10), []string{"/w0"})
	now = now.Add(unidentifiedWindow + time.Second)
	s.note(unidentifiedObs(9), []string{"/w5"})
	_, unids := split(s.list())
	if len(unids) != 3 {
		t.Fatalf("want pid 9 before the gap, pid 10, and pid 9 after it: %+v", unids)
	}
	byPID := map[int][]int{}
	for _, b := range unids {
		byPID[b.PID] = append(byPID[b.PID], b.Worktrees)
	}
	if w := byPID[9]; len(w) != 2 || w[0]+w[1] != 3 || (w[0] != 2 && w[1] != 2) {
		t.Fatalf("pid 9: %v", w)
	}
	if w := byPID[10]; len(w) != 1 || w[0] != 1 {
		t.Fatalf("pid 10 merged with pid 9: %v", w)
	}
	if !strings.Contains(unids[0].String(), "identity unreadable; seen continuously") {
		t.Fatalf("an unidentified entry must say how it was grouped: %s", unids[0])
	}
}

// A repeatedly met unidentified process takes one of the 64 slots, so it
// cannot evict other reports; distinct unidentified entries beyond the bound
// drop the oldest first, and never an identified blocker.
func TestBlockersUnidentifiedAreBoundedAndDoNotEvictByRepetition(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	s.note(identified(1, 777, "cwd"), []string{"/kept"})
	for pid := 100; pid < 100+maxUnidentified-1; pid++ {
		s.note(unidentifiedObs(pid), []string{"/w"})
	}
	for range 500 { // one live unidentified process met 500 times
		now = now.Add(time.Second)
		s.note(unidentifiedObs(9), []string{"/w"})
	}
	ids, unids := split(s.list())
	if len(ids) != 1 || len(unids) != maxUnidentified {
		t.Fatalf("repetition evicted reports: identified=%d unidentified=%d", len(ids), len(unids))
	}
	for pid := 200; pid < 205; pid++ { // five more distinct ones
		now = now.Add(time.Second)
		s.note(unidentifiedObs(pid), []string{"/w"})
	}
	ids, unids = split(s.list())
	if len(ids) != 1 || len(unids) != maxUnidentified {
		t.Fatalf("bound not kept: identified=%d unidentified=%d", len(ids), len(unids))
	}
	seen := map[int]bool{}
	for _, b := range unids {
		seen[b.PID] = true
	}
	for pid := 100; pid < 105; pid++ {
		if seen[pid] {
			t.Fatalf("pid %d, among the oldest, should have been dropped first", pid)
		}
	}
	if !seen[9] || !seen[204] {
		t.Fatal("the newest entries must be kept")
	}
}

// The count is every distinct workspace held, never capped: a process holding
// more worktrees than any bound is reported as holding all of them.
func TestBlockersCountEveryWorkspaceExactly(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	e := identified(9, 777, "cwd")
	const n = 20_001
	for i := range n {
		s.note(e, []string{"/home/u/.reliant/worktrees/p/w-" + strconv.Itoa(i), "/home/u/.reliant/worktrees/.reclaim/" + strconv.Itoa(i)})
	}
	s.note(e, []string{"/home/u/.reliant/worktrees/p/w-0"})
	if got := s.list(); len(got) != 1 || got[0].Worktrees != n {
		t.Fatalf("got %+v, want one blocker holding %d worktrees", got, n)
	}
}

// An age is shown only when the clocks allow one: a start time after the
// last sighting (the wall clock stepped back) shows none, never a negative.
func TestBlockerAgeIsNeverNegative(t *testing.T) {
	seen := time.Unix(1_800_000_000, 0)
	b := ReclaimBlocker{PID: 9, Comm: "x", Unreadable: "cwd", StartedAt: seen.Add(time.Hour), FirstSeen: seen, LastSeen: seen, Worktrees: 1}
	if line := b.String(); strings.Contains(line, "up ") || !strings.Contains(line, "pid 9 (x) blocks") {
		t.Fatalf("got %q", line)
	}
}

// A process whose status cannot be read is still one process when its stat
// can: the start time names it, so its holds aggregate.
func TestUninspectable_StatusUnreadableStillNamesTheProcess(t *testing.T) {
	pid := blockerPID()
	proc := uninspectableFixture(t, pid, "slot-holder", "slot-holder", argv("x"), pid, 777)
	if err := os.Chmod(filepath.Join(proc, strconv.Itoa(pid), "status"), 0); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{t.TempDir(), t.TempDir()} {
		err := checkProcessUseAt(context.Background(), proc, os.Geteuid(), []string{root})
		var u *UninspectableProcessError
		if !errors.As(err, &u) || u.StartTicks != 777 || u.Comm != "slot-holder" {
			t.Fatalf("an unreadable status must still hold, naming the process by its stat: %v", err)
		}
	}
	got, ok := blockerFor(pid)
	if !ok || got.Unidentified || got.Worktrees != 2 {
		t.Fatalf("got %+v", got)
	}
}
