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
