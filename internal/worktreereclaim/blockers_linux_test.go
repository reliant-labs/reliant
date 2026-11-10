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
		if u.PID != pid || u.Comm != "slot-holder" || u.What != "maps" || u.Started.IsZero() {
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

// A pid alone does not name a process (pids are reused): observations without
// a start time are never merged into one blocker, while one with a start time
// aggregates, and the same pid with another start time is another process.
func TestBlockersNeverMergeUnidentifiedObservationsByPID(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	started := now.Add(-time.Hour)
	s.note(&UninspectableProcessError{PID: 9, What: "cwd", Err: os.ErrPermission}, []string{"/a"})
	s.note(&UninspectableProcessError{PID: 9, What: "fd 3", Err: os.ErrPermission}, []string{"/b"})
	s.note(&UninspectableProcessError{PID: 9, Comm: "x", What: "cwd", Started: started, Err: os.ErrPermission}, []string{"/a"})
	s.note(&UninspectableProcessError{PID: 9, Comm: "x", What: "cwd", Started: started, Err: os.ErrPermission}, []string{"/b"})
	s.note(&UninspectableProcessError{PID: 9, Comm: "y", What: "cwd", Started: started.Add(time.Minute), Err: os.ErrPermission}, []string{"/c"})
	var unidentified, identified []ReclaimBlocker
	for _, b := range s.list() {
		if b.Unidentified {
			unidentified = append(unidentified, b)
		} else {
			identified = append(identified, b)
		}
	}
	if len(unidentified) != 2 || unidentified[0].Worktrees != 1 || unidentified[1].Worktrees != 1 {
		t.Fatalf("unidentified observations merged by pid: %+v", unidentified)
	}
	if !strings.Contains(unidentified[0].String(), "identity unreadable; one observation") {
		t.Fatalf("an unidentified observation must not read as a process: %s", unidentified[0])
	}
	if len(identified) != 2 || identified[0].Comm != "x" || identified[0].Worktrees != 2 || identified[1].Comm != "y" || identified[1].Worktrees != 1 {
		t.Fatalf("identified processes: %+v", identified)
	}
}

// Unidentified observations are bounded, oldest dropped first.
func TestBlockersUnidentifiedAreBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	for i := range maxUnidentified + 5 {
		now = now.Add(time.Second)
		s.note(&UninspectableProcessError{PID: 9, What: "cwd", Err: os.ErrPermission}, []string{"/w" + strconv.Itoa(i)})
	}
	got := s.list()
	if len(got) != maxUnidentified {
		t.Fatalf("%d unidentified observations kept, want %d", len(got), maxUnidentified)
	}
	oldest := got[0].FirstSeen
	for _, b := range got {
		if b.FirstSeen.Before(oldest) {
			oldest = b.FirstSeen
		}
	}
	if want := time.Unix(1_800_000_000, 0).Add(6 * time.Second); !oldest.Equal(want) {
		t.Fatalf("oldest kept %v, want %v (the five oldest dropped)", oldest, want)
	}
}

// The count is every distinct workspace held, never capped: a process holding
// more worktrees than any bound is reported as holding all of them.
func TestBlockersCountEveryWorkspaceExactly(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &blockerSet{now: func() time.Time { return now }}
	e := &UninspectableProcessError{PID: 9, Comm: "x", What: "cwd", Started: now.Add(-time.Hour), Err: os.ErrPermission}
	const n = 20_001
	for i := range n {
		s.note(e, []string{"/home/u/.reliant/worktrees/p/w-" + strconv.Itoa(i), "/home/u/.reliant/worktrees/.reclaim/" + strconv.Itoa(i)})
	}
	s.note(e, []string{"/home/u/.reliant/worktrees/p/w-0"})
	if got := s.list(); len(got) != 1 || got[0].Worktrees != n {
		t.Fatalf("got %+v, want one blocker holding %d worktrees", got, n)
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
		if !errors.As(err, &u) || u.Started.IsZero() || u.Comm != "slot-holder" {
			t.Fatalf("an unreadable status must still hold, naming the process by its stat: %v", err)
		}
	}
	got, ok := blockerFor(pid)
	if !ok || got.Unidentified || got.Worktrees != 2 {
		t.Fatalf("got %+v", got)
	}
}
