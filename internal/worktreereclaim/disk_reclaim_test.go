// Copyright (c) 2026 Reliant Labs
//go:build linux

package worktreereclaim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// archive locks a fixture worktree as archived under a fence at `at`, the way
// the server's archive protocol leaves it.
func archive(t *testing.T, path, id string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	info, err := inspectLock(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := setLock(ctx, info, path, ArchivedLockReason(id, fenceAt(at))); err != nil {
		t.Fatal(err)
	}
}

// An archived workspace is the archive protocol's while its archive is
// recent. Past ArchiveProtocolGrace the machine sweep judges its checkouts
// like any other: a clean, pushed one is captured (keyed by its row, so
// worktree.ensure restores it if the row is revived) and reclaimed, and one
// with work only it holds is proposed, never deleted.
func TestSweepReclaimsArchivedCheckoutsTheProtocolLeft(t *testing.T) {
	requireOpenat2(t)
	t.Parallel()
	heavyUnderShort(t, "~120 git processes")
	s := newSweepFixture(t)
	now := s.r.now()
	stale := s.worktree("row-stale", "feat/stale").Path
	archive(t, stale, "row-stale", now.Add(-ArchiveProtocolGrace-time.Hour))
	recent := s.worktree("row-recent", "feat/recent").Path
	archive(t, recent, "row-recent", now.Add(-time.Hour))
	work := s.worktree("row-work", "feat/work").Path
	write(t, filepath.Join(work, "wip.txt"), "only here\n")
	archive(t, work, "row-work", now.Add(-48*time.Hour))

	rep := s.r.SweepMachine(context.Background(), s.opts(SweepOn))
	e := entryFor(t, rep, stale)
	if e.Outcome != OutcomeReclaimed || e.CaptureRef == "" || exists(stale) {
		t.Fatalf("archived, clean and pushed, past the grace: %+v exists=%v; want reclaimed with a capture", e, exists(stale))
	}
	if _, ok := LatestCapture(context.Background(), s.project, "row-stale", stale); !ok {
		t.Fatal("the capture is not keyed by the row, so a revived row cannot restore it")
	}
	if e := entryFor(t, rep, recent); e.Reason != ReasonArchiveProtocol || !exists(filepath.Join(recent, ".git")) {
		t.Fatalf("recently archived: %+v; want left to the archive protocol", e)
	}
	if e := entryFor(t, rep, work); e.Class != ClassNeedsApproval || e.Outcome != OutcomeAwaiting || !exists(filepath.Join(work, "wip.txt")) {
		t.Fatalf("archived with work only it holds: %+v; want proposed and kept", e)
	}
	if info, err := inspectLock(context.Background(), work); err != nil || info.Reason != ArchivedLockReason("row-work", fenceAt(now.Add(-48*time.Hour))) {
		t.Fatalf("the proposed checkout's archive lock changed: %+v, %v", info, err)
	}
}

// A workspace whose directory changed after its archive only because a
// stopped retirement put a checkout back from its quarantine root, before such
// moves were recorded, is retired, not held as used after its archive
// forever. A change at the root after that put-back still counts.
func TestRetirementForgivesALegacyRollbackOfItsOwn(t *testing.T) {
	requireOpenat2(t)
	t.Parallel()
	heavyUnderShort(t, "a multi-repo workspace through a full retirement (~80 git processes)")
	for _, touched := range []bool{false, true} {
		t.Run(map[bool]string{false: "untouched", true: "touched after"}[touched], func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			w := f.multiRepo("legacy")
			archived := time.Now()
			w.Fence = fenceAt(archived)
			nextSecond(archived)
			// The put-back as it was: an entry leaves the quarantine root for
			// the workspace (both change), then its marker leaves the
			// quarantine root. No move stamp is written.
			q := f.r.quarantineRoot(w.ID)
			if err := os.MkdirAll(filepath.Join(q, "parked"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(q, "parked"), filepath.Join(w.Path, "parked")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(w.Path, "parked")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(q, "marker"), "x")
			if err := os.Remove(filepath.Join(q, "marker")); err != nil {
				t.Fatal(err)
			}
			if exists(f.r.ownMovePath(w.ID)) {
				t.Fatal("the fixture must look like a rollback that left no stamp")
			}
			if touched {
				nextSecond(time.Now())
				write(t, filepath.Join(w.Path, "late"), "x")
				if err := os.Remove(filepath.Join(w.Path, "late")); err != nil {
					t.Fatal(err)
				}
				if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUsedAfterArchive {
					t.Fatalf("a change at the root after the put-back must still hold: %+v", got)
				}
				return
			}
			if got := f.one(w); got.Outcome != OutcomeRemoved || exists(w.Path) {
				t.Fatalf("got %+v exists=%v; want the workspace retired", got, exists(w.Path))
			}
		})
	}
}

// Removing a directory (the node_modules strip) moves only its parent's
// mtime: that is not activity, and the walk's newest change ignores it.
func TestScanCheckoutIgnoresDirectoryMtimes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	file := filepath.Join(root, "web", "src", "a.ts")
	write(t, file, "x")
	if err := os.MkdirAll(filepath.Join(root, "web", "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, filepath.Join(root, "web", "src")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(root, "web", "node_modules")); err != nil {
		t.Fatal(err)
	}
	sc := scanCheckout(context.Background(), root)
	if !sc.Newest.Equal(old.Truncate(time.Second)) && sc.Newest.Sub(old).Abs() > time.Second {
		t.Fatalf("newest = %v, want the file's %v: a removed directory is not a change", sc.Newest, old)
	}
	now := time.Now()
	write(t, filepath.Join(root, "web", "src", "b.ts"), "new")
	if sc := scanCheckout(context.Background(), root); sc.Newest.Before(now.Add(-time.Second)) {
		t.Fatalf("a new file must count: newest = %v", sc.Newest)
	}
}

// A verdict that keeps a checkout is reused while its state is unchanged,
// and judged afresh when the state moves or the verdict ages out; a clean
// verdict is never cached.
func TestVerdictCacheReusesOnlyUnchangedKeepingVerdicts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	e := &SweepEntry{Key: "k", Path: "/w/a"}
	c := loadVerdicts(dir)
	c.put(e, cachedVerdict{Stamp: "s1", At: now, Reason: ReasonUnpushed, Fingerprint: "fp", Bytes: 7})
	c.put(&SweepEntry{Key: "safe", Path: "/w/b"}, cachedVerdict{Stamp: "s1", At: now})
	c.put(&SweepEntry{Key: "unv", Path: "/w/c"}, cachedVerdict{Stamp: "s1", At: now, Reason: ReasonUnverified})
	c.save()

	c = loadVerdicts(dir)
	if v, ok := c.get(e, "s1", now.Add(time.Hour)); !ok || v.Fingerprint != "fp" {
		t.Fatalf("same state within the TTL: %+v %v", v, ok)
	}
	if _, ok := c.get(e, "s2", now.Add(time.Hour)); ok {
		t.Fatal("a changed state reused the old verdict")
	}
	if _, ok := c.get(e, "s1", now.Add(verdictTTL)); ok {
		t.Fatal("an aged verdict was reused")
	}
	for _, k := range []string{"safe", "unv"} {
		if _, ok := c.get(&SweepEntry{Key: k, Path: "/w/" + map[string]string{"safe": "b", "unv": "c"}[k]}, "s1", now); ok {
			t.Fatalf("%s verdict was cached", k)
		}
	}
	if got := c.bytesOf(e); got != 7 {
		t.Fatalf("bytes = %d", got)
	}
	// An entry a pass did not look at survives its save.
	c2 := loadVerdicts(dir)
	c2.keepUnvisited(now.Add(time.Hour))
	c2.save()
	var doc verdictsDoc
	b, _ := os.ReadFile(filepath.Join(dir, verdictsFile))
	if err := json.Unmarshal(b, &doc); err != nil || len(doc.Verdicts) != 1 {
		t.Fatalf("after a pass that visited nothing: %+v, %v", doc, err)
	}
}

// The deep checks reach first what the last pass did not, then (under
// pressure) the largest: no checkout waits more than one pass.
func TestOrderDeepReachesTheUnreachedFirstThenTheLargest(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	entries := []SweepEntry{
		{Key: "small", Path: "/w/small"},
		{Key: "big", Path: "/w/big"},
		{Key: "deferred", Path: "/w/deferred"},
		{Key: "new", Path: "/w/new"},
	}
	activity := []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour), t0.Add(3 * time.Hour)}
	last := map[string]SweepEntry{}
	for _, e := range []SweepEntry{
		{Key: "small", Path: "/w/small", Outcome: OutcomeAwaiting, Bytes: 1},
		{Key: "big", Path: "/w/big", Outcome: OutcomeAwaiting, Bytes: 100},
		{Key: "deferred", Path: "/w/deferred", Outcome: OutcomeSweepDeferred},
	} {
		last[verdictKey(&e)] = e
	}
	deep := []int{0, 1, 2, 3}
	orderDeep(deep, entries, activity, last, nil, true)
	var got []string
	for _, i := range deep {
		got = append(got, entries[i].Key)
	}
	if want := []string{"deferred", "new", "big", "small"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pressure order %v, want %v", got, want)
	}
	deep = []int{0, 1, 2, 3}
	orderDeep(deep, entries, activity, last, nil, false)
	got = got[:0]
	for _, i := range deep {
		got = append(got, entries[i].Key)
	}
	if want := []string{"deferred", "new", "small", "big"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if n := sweepWorkerCount(true); n < sweepWorkers || n > 16 {
		t.Fatalf("pressure workers = %d", n)
	}
	if n := sweepWorkerCount(false); n != sweepWorkers {
		t.Fatalf("workers = %d", n)
	}
}
