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
			// starts in a later second than it. This orders nothing
			// concurrent (every step is sequential); it only keeps the
			// change and the archive out of one reflog second.
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

// nextSecond waits until the wall clock is in a later whole second than t,
// which is all a reflog or file time can distinguish.
func nextSecond(t time.Time) {
	time.Sleep(time.Until(t.Truncate(time.Second).Add(2 * time.Second)))
}

// The reclaimer's own shedding is not use: an archived worktree held for its
// work has its node_modules shed after the archive, which moves the checkout's
// mtime; once the hold clears it is retired, not held as used forever (#2571
// review, B1).
func TestWholeRetirementItsOwnSheddingIsNotUseAfterArchive(t *testing.T) {
	heavyUnderShort(t, "temporary real Git whole-worktree retirement with shedding")
	f := newFixture(t)
	f.idle()
	ignore(t, f, "node_modules/\n")
	w := f.worktree("shed", "feat/shed")
	w.ShedAfterSeconds = 3600
	modules := filepath.Join(w.Path, "node_modules", "pkg", "index.js")
	write(t, modules, "regenerable")
	write(t, filepath.Join(w.Path, "work.txt"), "committed, not yet pushed")
	run(t, w.Path, "add", "work.txt")
	run(t, w.Path, "commit", "-q", "-m", "work")
	archived := time.Now()
	w.Fence = fenceAt(archived)
	nextSecond(archived)

	got := f.r.Reconcile(t.Context(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed || exists(filepath.Dir(modules)) || got.StrippedBytes == 0 {
		t.Fatalf("want held as unpushed with node_modules shed: %+v", got)
	}
	// The hold clears with nobody in the worktree: its branch lands upstream.
	run(t, w.Path, "push", "-q", "origin", "feat/shed")
	if got := f.one(w); got.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("the reclaimer's own shedding held the worktree: %+v", got)
	}
	if exists(f.r.ownWritePath(w.ID)) {
		t.Fatal("the shedding stamp outlived the worktree")
	}
}

// The veto starts ArchiveClockSkew before the archive time, so use on a
// machine whose clock trails the database's is still caught; the reclaimer's
// own last shedding moves it later.
func TestUseCountsAfterAllowsForClockSkewAndOwnWrites(t *testing.T) {
	r := &Reclaimer{Root: t.TempDir(), ArchiveClockSkew: 10 * time.Minute}
	archived := time.Date(2026, 11, 10, 13, 41, 44, 0, time.UTC)
	w := Worktree{ID: "row", Fence: fenceAt(archived)}
	if at, ok := r.useCountsAfter(w); !ok || !at.Equal(archived.Add(-10*time.Minute)) {
		t.Fatalf("got %v %v, want the archive time less the skew", at, ok)
	}
	shed := archived.Add(time.Hour)
	if err := os.MkdirAll(filepath.Join(r.Root, ".reclaim"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, r.ownWritePath(w.ID), strconv.FormatInt(shed.UnixNano(), 10))
	if at, ok := r.useCountsAfter(w); !ok || !at.Equal(shed) {
		t.Fatalf("got %v %v, want the end of the reclaimer's own shedding", at, ok)
	}
	if _, ok := r.useCountsAfter(Worktree{ID: "row", Fence: "100"}); ok {
		t.Fatal("a fence with no archive time has no veto")
	}
}
