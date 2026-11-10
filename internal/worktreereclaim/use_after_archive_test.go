package worktreereclaim

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fenceAt(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

// A worktree archived, then used (an agent working in it by path, a chat
// paused mid-run: activity the server's archive could not see), is held and
// kept, however long it has since been quiet (Reliant bug 7576a754).
func TestWholeRetirementHoldsAWorktreeUsedAfterItWasArchived(t *testing.T) {
	heavyUnderShort(t, "temporary real Git whole-worktree retirement")
	// Each kind is seen by one check alone: a commit (reflog only) by the
	// quiet-time check before anything is parked, a timestamp moved deep in
	// the tree (no directory or git state changes) by the retirement
	// inventory.
	for _, kind := range []string{"file", "branch-switch", "commit-only", "deep-touch"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			w := f.worktree("used", "feat/used")
			deep := filepath.Join(w.Path, "deep", "er", "f.txt")
			write(t, deep, "before the archive")
			run(t, w.Path, "add", "deep")
			run(t, w.Path, "commit", "-q", "-m", "deep")
			run(t, w.Path, "push", "-q", "origin", "feat/used")
			archived := time.Now()
			w.Fence = fenceAt(archived)
			// The reflog records whole seconds: what follows the archive
			// starts in a later second than it.
			time.Sleep(time.Until(archived.Truncate(time.Second).Add(2 * time.Second)))
			switch kind {
			case "file":
				// Committed and pushed, so the work is safe; only the timing
				// says someone is still in it.
				write(t, filepath.Join(w.Path, "after.txt"), "written after the archive")
				run(t, w.Path, "add", "after.txt")
				run(t, w.Path, "commit", "-q", "-m", "after")
				run(t, w.Path, "push", "-q", "origin", "feat/used")
			case "branch-switch":
				run(t, w.Path, "checkout", "-q", "-b", "feat/next", "main")
				run(t, w.Path, "push", "-q", "origin", "feat/next")
			case "commit-only":
				run(t, w.Path, "commit", "-q", "--allow-empty", "-m", "after")
				run(t, w.Path, "push", "-q", "origin", "feat/used")
				// Back-date what the commit touched outside .git, so only the
				// reflog shows it.
				old := archived.Add(-time.Hour)
				for _, p := range []string{w.Path, filepath.Join(w.Path, "deep"), filepath.Join(w.Path, "deep", "er"), deep} {
					if err := os.Chtimes(p, old, old); err != nil {
						t.Fatal(err)
					}
				}
			case "deep-touch":
				now := time.Now()
				if err := os.Chtimes(deep, now, now); err != nil {
					t.Fatal(err)
				}
			}
			got := f.one(w)
			if got.Outcome == OutcomeRemoved || !exists(filepath.Join(w.Path, ".git")) {
				t.Fatalf("a worktree used after its archive was retired: %+v", got)
			}
			if got.Reason != ReasonUsedAfterArchive || !strings.Contains(got.Detail, "used after it was archived") {
				t.Fatalf("held for the wrong reason: %+v", got)
			}
			if q := f.r.quarantined(w.ID); len(q) != 0 {
				t.Fatalf("parked although it must stay home: %v", q)
			}
		})
	}
}

// Untouched since its archive, the same worktree is retired as before.
func TestWholeRetirementRetiresAWorktreeQuietSinceItsArchive(t *testing.T) {
	heavyUnderShort(t, "temporary real Git whole-worktree retirement")
	f := newFixture(t)
	f.idle()
	w := f.worktree("quiet", "feat/quiet")
	w.Fence = fenceAt(time.Now().Add(time.Second))
	if got := f.one(w); got.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("a worktree quiet since its archive must be retired: %+v", got)
	}
}

func TestArchiveTimeReadsOnlyRealArchiveTimes(t *testing.T) {
	at := time.Date(2026, 11, 10, 13, 41, 44, 419973000, time.UTC)
	if got, ok := archiveTime("1794318104419973000"); !ok || !got.Equal(at) {
		t.Fatalf("got %v %v", got, ok)
	}
	for _, f := range []string{"", "100", "fence", "-1"} {
		if _, ok := archiveTime(f); ok {
			t.Errorf("%q is not an archive time", f)
		}
	}
}
