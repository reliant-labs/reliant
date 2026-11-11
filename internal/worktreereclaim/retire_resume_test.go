package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// manyFiles commits n tracked files to main and pushes them, so a worktree
// made after it is clean, pushed and has a tree worth unlinking.
func manyFiles(t *testing.T, f *fixture, n int) {
	t.Helper()
	for i := range n {
		write(t, filepath.Join(f.project, "files", fmt.Sprintf("f%03d.txt", i)), "content")
	}
	run(t, f.project, "add", "files")
	run(t, f.project, "commit", "-q", "-m", "files")
	run(t, f.project, "push", "-q", "origin", "main")
}

// interruptedRetirement retires w with a process check that fails partway
// through the disposal, as the request deadline or a passing process did on
// real machines, and returns the one parked checkout it leaves behind.
func interruptedRetirement(t *testing.T, f *fixture, w Worktree) string {
	t.Helper()
	f.r.DisposalCheckEvery = 1 // every entry, so the failure lands mid-tree
	calls := 0
	f.r.ProcessUse = func(context.Context, []string) error {
		for _, q := range f.r.quarantined(w.ID) {
			if exists(q + ".disposing") {
				calls++
				if calls == 5 {
					return errors.New("workspace open by process 4242")
				}
			}
		}
		return nil
	}
	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonQuarantined {
		t.Fatalf("interrupted retirement: %+v", got)
	}
	parked := f.r.quarantined(w.ID)
	if len(parked) != 1 || !exists(parked[0]+".disposing") || !exists(filepath.Join(parked[0], ".git")) {
		t.Fatalf("want one partially disposed checkout, got %v", parked)
	}
	left, err := os.ReadDir(filepath.Join(parked[0], "files"))
	if err != nil || len(left) == 0 || len(left) >= 40 {
		t.Fatalf("want a partial disposal, %d of 40 files left (%v)", len(left), err)
	}
	f.r.ProcessUse = func(context.Context, []string) error { return nil }
	f.r.DisposalCheckEvery = 0
	return parked[0]
}

func TestWholeRetirementResumesAnInterruptedDisposal(t *testing.T) {
	heavyUnderShort(t, "temporary real Git whole-worktree retirement, interrupted and resumed")
	f := newFixture(t)
	manyFiles(t, f, 40)
	f.idle()
	w := f.worktree("resume", "feat/resume")
	q := interruptedRetirement(t, f, w)

	// Before the fix every later pass answered held/quarantined, forever.
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeClaimed {
		t.Fatalf("phase one on an interrupted retirement: %+v", got)
	}
	if !exists(filepath.Join(q, ".git")) {
		t.Fatal("phase one deleted something; only phase two may")
	}
	w.Remove = true
	got = f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("resumed retirement: %+v", got)
	}
	if exists(q) || exists(q+".disposing") || exists(originMarker(q)) || exists(f.r.quarantineRoot(w.ID)) {
		t.Fatal("quarantine left behind after a resumed retirement")
	}
	if list := run(t, f.project, "worktree", "list", "--porcelain"); strings.Contains(list, q) || strings.Contains(list, w.Path) {
		t.Fatalf("registration left behind:\n%s", list)
	}
	if run(t, f.project, "branch", "--list", "feat/resume") == "" {
		t.Fatal("the branch was deleted")
	}
}

func TestWholeRetirementResumeHoldsWorkWrittenAfterTheInterruption(t *testing.T) {
	heavyUnderShort(t, "temporary real Git whole-worktree retirement, interrupted and changed")
	for _, kind := range []string{"untracked", "edited", "staged", "occupied", "foreign-fence"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			manyFiles(t, f, 40)
			f.idle()
			w := f.worktree("held", "feat/held")
			q := interruptedRetirement(t, f, w)
			keep := filepath.Join(q, "files", "f039.txt") // deleted last: deepest-first, then by name
			switch kind {
			case "untracked":
				keep = filepath.Join(q, "new-work.txt")
				write(t, keep, "written after the interruption")
			case "edited":
				write(t, keep, "edited after the interruption")
			case "staged":
				write(t, keep, "staged after the interruption")
				run(t, q, "add", "files/f039.txt")
			case "occupied":
				if err := os.MkdirAll(w.Path, 0o755); err != nil {
					t.Fatal(err)
				}
				keep = w.Path
			case "foreign-fence":
				w.Fence = "200"
			}
			for _, remove := range []bool{false, true} {
				w.Remove = remove
				got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
				if got.Outcome != OutcomeHeld || got.Reason != ReasonQuarantined {
					t.Fatalf("remove=%v: changed checkout not held: %+v", remove, got)
				}
			}
			if !exists(keep) || !exists(filepath.Join(q, ".git")) || !exists(q+".disposing") {
				t.Fatal("held checkout lost content or its journal")
			}
		})
	}
}

func TestWholeRetirementPacesItsProcessChecks(t *testing.T) {
	heavyUnderShort(t, "temporary real Git whole-worktree retirement of a 400-file checkout")
	f := newFixture(t)
	manyFiles(t, f, 400)
	f.idle()
	w := f.worktree("paced", "feat/paced")
	calls := 0
	f.r.ProcessUse = func(context.Context, []string) error { calls++; return nil }
	got := f.one(w)
	if got.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("retirement: %+v", got)
	}
	// One process scan per unlinked entry (~400 here) is what made a real
	// checkout's disposal outlast every request deadline.
	if calls > 40 {
		t.Fatalf("%d process checks to retire a 400-file checkout; want them paced", calls)
	}
}
