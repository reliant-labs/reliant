// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pruneNow is the clock of every prune test.
var pruneNow = time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

// pruneHost is a Host over an empty Root whose clock reads pruneNow.
func pruneHost(t *testing.T) *Host {
	t.Helper()
	return &Host{Root: t.TempDir(), now: func() time.Time { return pruneNow }}
}

// fakeCache writes a job cache of size bytes for repo ("owner/name"), last
// used at lastUsed (its lease's mtime), and returns its directory.
func fakeCache(t *testing.T, h *Host, repo string, size int, lastUsed time.Time) string {
	t.Helper()
	owner, name, _ := strings.Cut(repo, "/")
	dir := filepath.Join(h.Root, owner, name+".git")
	if err := os.MkdirAll(filepath.Join(dir, "objects", "pack"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "objects", "pack", "pack-1.pack"), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leasePath(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(leasePath(dir), lastUsed, lastUsed); err != nil {
		t.Fatal(err)
	}
	return dir
}

func outcomes(res PruneResult) map[string]string {
	out := map[string]string{}
	for _, c := range res.Caches {
		out[c.Repository] = c.Outcome
	}
	return out
}

func assertGone(t *testing.T, dir string) {
	t.Helper()
	for _, p := range []string{dir, leasePath(dir)} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err %v)", p, err)
		}
	}
}

func assertKept(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "objects", "pack", "pack-1.pack")); err != nil {
		t.Errorf("%s was removed: %v", dir, err)
	}
}

func TestPruneRemovesACacheUnusedForMaxAge(t *testing.T) {
	t.Parallel()
	h := pruneHost(t)
	stale := fakeCache(t, h, "acme/stale", 100, pruneNow.Add(-8*24*time.Hour))
	fresh := fakeCache(t, h, "acme/fresh", 100, pruneNow.Add(-6*24*time.Hour))

	res := h.Prune(t.Context(), PrunePolicy{})

	if got := outcomes(res); got["acme/stale"] != PruneRemovedAge || got["acme/fresh"] != PruneKept {
		t.Fatalf("outcomes: %v", got)
	}
	if res.Freed != 100 || res.Bytes != 200 {
		t.Fatalf("freed %d of %d, want 100 of 200", res.Freed, res.Bytes)
	}
	assertGone(t, stale)
	assertKept(t, fresh)
	// Nothing left aside.
	entries, _ := os.ReadDir(filepath.Join(h.Root, "acme"))
	for _, e := range entries {
		if strings.Contains(e.Name(), trashMarker) {
			t.Errorf("trash left behind: %s", e.Name())
		}
	}
}

func TestPruneJudgesACacheFromBeforeLeasesByItsFetches(t *testing.T) {
	t.Parallel()
	h := pruneHost(t)
	old := pruneNow.Add(-30 * 24 * time.Hour)
	dir := fakeCache(t, h, "acme/legacy", 10, old)
	if err := os.Remove(leasePath(dir)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "objects", "pack", "pack-1.pack"), filepath.Join(dir, "objects", "pack"), filepath.Join(dir, "objects"), dir} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if got := outcomes(h.Prune(t.Context(), PrunePolicy{})); got["acme/legacy"] != PruneRemovedAge {
		t.Fatalf("outcomes: %v", got)
	}
	assertGone(t, dir)
}

func TestPruneEvictsTheLeastRecentlyUsedOverBudget(t *testing.T) {
	t.Parallel()
	h := pruneHost(t)
	oldest := fakeCache(t, h, "acme/a", 400, pruneNow.Add(-5*time.Hour))
	middle := fakeCache(t, h, "acme/b", 400, pruneNow.Add(-3*time.Hour))
	newest := fakeCache(t, h, "acme/c", 400, pruneNow.Add(-2*time.Hour))

	// 1200 bytes over a 1000-byte budget: down to its low water, 800, which
	// the oldest alone buys.
	res := h.Prune(t.Context(), PrunePolicy{MaxBytes: 1000})

	if got := outcomes(res); got["acme/a"] != PruneRemovedSize || got["acme/b"] != PruneKept || got["acme/c"] != PruneKept {
		t.Fatalf("outcomes: %v", got)
	}
	assertGone(t, oldest)
	assertKept(t, middle)
	assertKept(t, newest)
}

func TestPruneSizeEvictionSparesACacheUsedWithinMinIdle(t *testing.T) {
	t.Parallel()
	h := pruneHost(t)
	idle := fakeCache(t, h, "acme/idle", 400, pruneNow.Add(-2*time.Hour))
	recent := fakeCache(t, h, "acme/recent", 400, pruneNow.Add(-10*time.Minute))

	res := h.Prune(t.Context(), PrunePolicy{MaxBytes: 100, MinIdle: time.Hour})

	if got := outcomes(res); got["acme/idle"] != PruneRemovedSize || got["acme/recent"] != PruneKept {
		t.Fatalf("outcomes: %v", got)
	}
	assertGone(t, idle)
	assertKept(t, recent)
}

func TestPruneSkipsACacheAJobOfThisProcessHolds(t *testing.T) {
	t.Parallel()
	h := pruneHost(t)
	dir := fakeCache(t, h, "acme/busy", 100, pruneNow.Add(-8*24*time.Hour))
	lock := h.repoLock("Acme/Busy")
	lock.Lock()

	res := h.Prune(t.Context(), PrunePolicy{MaxBytes: 1})
	lock.Unlock()

	if got := outcomes(res); got["acme/busy"] != PruneInUse {
		t.Fatalf("outcomes: %v", got)
	}
	assertKept(t, dir)
}

func TestPruneSkipsACacheAnotherProcessesJobHolds(t *testing.T) {
	t.Parallel()
	h := pruneHost(t)
	dir := fakeCache(t, h, "acme/busy", 100, pruneNow.Add(-8*24*time.Hour))
	// Another daemon's job: the lease's file lock, through a file of its own.
	lease, err := acquireLease(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	res := h.Prune(t.Context(), PrunePolicy{MaxBytes: 1})
	releaseLease(lease)

	if got := outcomes(res); got["acme/busy"] != PruneInUse {
		t.Fatalf("outcomes: %v", got)
	}
	assertKept(t, dir)

	// Released, the next pass removes it.
	if got := outcomes(h.Prune(t.Context(), PrunePolicy{})); got["acme/busy"] != PruneRemovedAge {
		t.Fatalf("after release: %v", got)
	}
	assertGone(t, dir)
}

func TestPruneClearsWhatAnInterruptedPassLeft(t *testing.T) {
	t.Parallel()
	h := pruneHost(t)
	trash := filepath.Join(h.Root, "acme", "gone.git"+trashMarker+"1")
	if err := os.MkdirAll(filepath.Join(trash, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(h.Root, "acme", "orphan.git")
	if err := os.WriteFile(leasePath(orphan), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := pruneNow.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(leasePath(orphan), old, old); err != nil {
		t.Fatal(err)
	}

	if res := h.Prune(t.Context(), PrunePolicy{}); len(res.Caches) != 0 {
		t.Fatalf("caches: %+v", res.Caches)
	}
	assertGone(t, trash)
	assertGone(t, orphan)
}

func TestLeaseTakesTheFileAtItsPathAfterAPruneUnlinkedIt(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "acme", "x.git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := acquireLease(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// Prune's removal: the lease unlinked under its lock.
	if err := os.Remove(leasePath(dir)); err != nil {
		t.Fatal(err)
	}
	second, ok, err := tryLease(dir)
	if err != nil || !ok {
		t.Fatalf("tryLease after the unlink: ok %v, err %v", ok, err)
	}
	held, _ := second.Stat()
	cur, err := os.Stat(leasePath(dir))
	if err != nil || !os.SameFile(held, cur) {
		t.Fatalf("the lease held is not the one at its path (err %v)", err)
	}
	releaseLease(second)
	releaseLease(first)
}
