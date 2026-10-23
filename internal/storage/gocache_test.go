package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/commitpolicy"
	"github.com/reliant-labs/forge/internal/openfiles"
)

func hexName(n int, suffix string) string {
	return strings.Repeat("a", 63) + string(rune('0'+n%10)) + suffix
}

func makeBuildCache(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(root, "README")
	if err := os.WriteFile(readme, []byte(commitpolicy.GoBuildCacheSentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-1000 * time.Hour)
	_ = os.Chtimes(readme, at, at)
}

func putEntry(t *testing.T, root, sub, name string, size int, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func goRunner(t *testing.T, out *strings.Builder) Runner {
	p := DefaultPolicy()
	return Runner{Policy: p, Out: out, Ctx: context.Background()}
}

func TestSharedGoCacheAgeAndFloor(t *testing.T) {
	root := t.TempDir()
	makeBuildCache(t, root)
	old := putEntry(t, root, "ab", hexName(1, "-a"), 10, 72*time.Hour)
	mid := putEntry(t, root, "ab", hexName(2, "-d"), 10, 10*time.Hour)
	fresh := putEntry(t, root, "cd", hexName(3, "-a"), 10, 30*time.Minute)
	trim := filepath.Join(root, "trim.txt")
	if err := os.WriteFile(trim, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-1000 * time.Hour)
	_ = os.Chtimes(trim, at, at)
	junk := putEntry(t, root, "ab", "notanentry", 5, 1000*time.Hour)

	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	if err := r.SharedGoCache(false); err != nil {
		t.Fatal(err)
	}
	if !exists(old) || !strings.Contains(out.String(), "would remove") {
		t.Fatalf("dry-run must list but not delete:\n%s", out.String())
	}
	if err := r.SharedGoCache(true); err != nil {
		t.Fatal(err)
	}
	if exists(old) || !exists(mid) || !exists(fresh) || !exists(trim) || !exists(junk) {
		t.Fatalf("old=%v mid=%v fresh=%v trim=%v junk=%v", exists(old), exists(mid), exists(fresh), exists(trim), exists(junk))
	}
}

func TestSharedGoCacheUnsetRefusedUnderTest(t *testing.T) {
	var out strings.Builder
	r := goRunner(t, &out)
	if err := r.SharedGoCache(true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unset under test") {
		t.Fatalf("expected refusal: %q", out.String())
	}
}

func TestSharedGoCacheNeedsSentinel(t *testing.T) {
	root := t.TempDir()
	old := putEntry(t, root, "ab", hexName(1, "-a"), 10, 72*time.Hour)
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	if err := r.SharedGoCache(true); err != nil {
		t.Fatal(err)
	}
	if !exists(old) {
		t.Fatal("a directory without the cache README must not be trimmed")
	}
}

func TestGolangciLintCache(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("This directory holds cached build artifacts from golangci-lint.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := putEntry(t, root, "00", hexName(1, "-a"), 10, 72*time.Hour)
	fresh := putEntry(t, root, "00", hexName(2, "-a"), 10, time.Minute)
	var out strings.Builder
	r := goRunner(t, &out)
	r.GolangciCacheRoot = root
	if err := r.GolangciLintCache(true); err != nil {
		t.Fatal(err)
	}
	if exists(old) || !exists(fresh) {
		t.Fatal("golangci-lint cache not trimmed correctly")
	}
}

func TestGoimportsIndexKeepsLinkedAndRecent(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		_ = os.Chtimes(p, at, at)
		return p
	}
	current := mk("index-0-1", 500*time.Hour) // old but linked
	stale := mk("index-0-2", 500*time.Hour)
	recent := mk("index-0-3", time.Hour)
	if err := os.WriteFile(filepath.Join(root, "index-name-0-abc"), []byte("index-0-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoimportsRoot = root
	if err := r.GoimportsIndex(false); err != nil || !exists(stale) {
		t.Fatalf("dry-run deleted: %v", err)
	}
	if err := r.GoimportsIndex(true); err != nil {
		t.Fatal(err)
	}
	if exists(stale) || !exists(current) || !exists(recent) {
		t.Fatalf("stale=%v current=%v recent=%v", exists(stale), exists(current), exists(recent))
	}
}

func TestGoimportsIndexWithoutLinksKeepsNewestTwo(t *testing.T) {
	root := t.TempDir()
	var paths []string
	for i, age := range []time.Duration{900, 800, 700, 600} {
		p := filepath.Join(root, "index-0-"+string(rune('1'+i)))
		_ = os.WriteFile(p, []byte("x"), 0o600)
		at := time.Now().Add(-age * time.Hour)
		_ = os.Chtimes(p, at, at)
		paths = append(paths, p)
	}
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoimportsRoot = root
	if err := r.GoimportsIndex(true); err != nil {
		t.Fatal(err)
	}
	if exists(paths[0]) || exists(paths[1]) || !exists(paths[2]) || !exists(paths[3]) {
		t.Fatal("expected only the newest two kept")
	}
}

// orphan fixtures

type orphanFixture struct {
	scan, shared, orphan, young, live, held string
	r                                       Runner
	out                                     *strings.Builder
}

func newOrphanFixture(t *testing.T, procText string, open []string, procErr error) orphanFixture {
	t.Helper()
	scan := t.TempDir()
	scan = resolved(t, scan)
	f := orphanFixture{scan: scan, out: &strings.Builder{}}
	f.shared = filepath.Join(scan, "shared-gocache")
	makeBuildCache(t, f.shared)
	putEntry(t, f.shared, "ab", hexName(1, "-a"), 10, 100*time.Hour)

	f.orphan = filepath.Join(scan, ".gocache-old")
	makeBuildCache(t, f.orphan)
	putEntry(t, f.orphan, "ab", hexName(1, "-a"), 10, 100*time.Hour)

	f.young = filepath.Join(scan, ".gocache-young")
	makeBuildCache(t, f.young)
	putEntry(t, f.young, "ab", hexName(1, "-a"), 10, time.Hour)

	f.live = filepath.Join(scan, ".gocache-live")
	makeBuildCache(t, f.live)
	putEntry(t, f.live, "ab", hexName(1, "-a"), 10, 100*time.Hour)

	f.held = filepath.Join(scan, "held-gocache")
	makeBuildCache(t, f.held)
	heldFile := putEntry(t, f.held, "ab", hexName(1, "-a"), 10, 100*time.Hour)

	r := goRunner(t, f.out)
	r.GoCacheRoot = f.shared
	r.GoModCacheRoot = filepath.Join(scan, "shared-mod")
	r.Policy.HostPaths = []string{scan}
	r.ProcessEnv = func(context.Context) (string, error) {
		if procErr != nil {
			return "", procErr
		}
		return strings.ReplaceAll(procText, "LIVE", f.live), nil
	}
	paths := open
	r.OpenPaths = func(context.Context) (openfiles.Snapshot, error) {
		return openfiles.FromPaths(append(paths, heldFile)), nil
	}
	f.r = r
	return f
}

func TestOrphanedGoCachesReapsOnlyIdleUnreferenced(t *testing.T) {
	f := newOrphanFixture(t, "FOO=1 GOCACHE=LIVE/ go build\n", nil, nil)
	if err := f.r.OrphanedGoCaches(false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.orphan, f.young, f.live, f.held, f.shared} {
		if !exists(p) {
			t.Fatalf("dry-run removed %s", p)
		}
	}
	if !strings.Contains(f.out.String(), "would remove build cache "+f.orphan) {
		t.Fatalf("dry-run did not list the orphan:\n%s", f.out.String())
	}
	if err := f.r.OrphanedGoCaches(true); err != nil {
		t.Fatal(err)
	}
	if exists(f.orphan) {
		t.Fatal("orphan not reaped")
	}
	for _, p := range []string{f.young, f.live, f.held, f.shared} {
		if !exists(p) {
			t.Fatalf("wrongly removed %s", p)
		}
	}
}

func TestOrphanedGoCachesFailClosed(t *testing.T) {
	f := newOrphanFixture(t, "", nil, os.ErrPermission)
	if err := f.r.OrphanedGoCaches(true); err != nil {
		t.Fatal(err)
	}
	if !exists(f.orphan) || !strings.Contains(f.out.String(), "skipped") {
		t.Fatal("unreadable process listing must skip the layer")
	}
	f = newOrphanFixture(t, "x", nil, nil)
	f.r.OpenPaths = func(context.Context) (openfiles.Snapshot, error) { return openfiles.Snapshot{}, os.ErrPermission }
	if err := f.r.OrphanedGoCaches(true); err != nil {
		t.Fatal(err)
	}
	if !exists(f.orphan) {
		t.Fatal("failed lsof must skip the layer")
	}
}

func TestOrphanedGoCachesModCacheAndEmptyParent(t *testing.T) {
	f := newOrphanFixture(t, "x", nil, nil)
	holder := filepath.Join(f.scan, ".gocache-both")
	build := filepath.Join(holder, "build")
	makeBuildCache(t, build)
	putEntry(t, build, "ab", hexName(1, "-a"), 10, 100*time.Hour)
	mod := filepath.Join(holder, "mod")
	dl := filepath.Join(mod, "cache", "download", "example.com", "m", "@v")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(dl, "v1.zip")
	_ = os.WriteFile(ro, []byte("z"), 0o444)
	at := time.Now().Add(-100 * time.Hour)
	_ = os.Chtimes(ro, at, at)
	_ = os.Chmod(dl, 0o555)
	if err := f.r.OrphanedGoCaches(true); err != nil {
		t.Fatal(err)
	}
	if exists(holder) {
		t.Fatal("holder with only build+mod must be removed entirely")
	}
	if !exists(f.scan) || !exists(f.shared) {
		t.Fatal("scan root or shared cache removed")
	}
}

func TestOrphanedGoCachesNeverTouchesSharedAncestors(t *testing.T) {
	f := newOrphanFixture(t, "x", nil, nil)
	// A recognised cache that CONTAINS the shared cache must be left alone.
	outer := filepath.Join(f.scan, "outer")
	makeBuildCache(t, outer)
	putEntry(t, outer, "ab", hexName(1, "-a"), 10, 100*time.Hour)
	f.r.GoCacheRoot = filepath.Join(outer, "inner")
	makeBuildCache(t, f.r.GoCacheRoot)
	if err := f.r.OrphanedGoCaches(true); err != nil {
		t.Fatal(err)
	}
	if !exists(outer) || !exists(f.r.GoCacheRoot) {
		t.Fatal("cache containing the shared cache was removed")
	}
}

func TestOrphanedGoCachesRefusedUnderTestWhenUnset(t *testing.T) {
	var out strings.Builder
	r := goRunner(t, &out)
	if err := r.OrphanedGoCaches(true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skip") {
		t.Fatalf("unset roots must be refused: %q", out.String())
	}
}

func TestOrphanedGoCachesStopsAtDeadline(t *testing.T) {
	f := newOrphanFixture(t, "x", nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.r.Ctx = ctx
	_ = f.r.OrphanedGoCaches(true)
	if !exists(f.orphan) {
		t.Fatal("removed after the deadline")
	}
}

// fillShards writes n entries per shard across `shards` shard dirs, each aged
// `age`, and returns the root.
func fillShards(t *testing.T, root string, shards, perShard int, age func(shard, i int) time.Duration) {
	t.Helper()
	makeBuildCache(t, root)
	for s := 0; s < shards; s++ {
		for i := 0; i < perShard; i++ {
			putEntry(t, root, fmt.Sprintf("%02x", s), fmt.Sprintf("%063x%d-a", s*1000+i, i%10), 100, age(s, i))
		}
	}
}

func TestGoCacheCutoffNeverBelowFloor(t *testing.T) {
	now := time.Now()
	mk := func(ages ...time.Duration) goCacheSample {
		var s goCacheSample
		s.shards = 1
		for _, a := range ages {
			s.bytes += 100
			s.entries = append(s.entries, goCacheEntry{size: 100, mtime: now.Add(-a)})
		}
		sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].mtime.Before(s.entries[j].mtime) })
		return s
	}
	const minAge = defaultGoCacheMinAge
	// Way over budget, but every sampled entry is 1m old (inside the minimum
	// age): the cutoff must still be no newer than now-minAge.
	c, _ := goCacheCutoff(mk(time.Minute, time.Minute, time.Minute), 1, now, 48*time.Hour, 1, minAge)
	if c.After(now.Add(-minAge)) {
		t.Fatalf("cutoff %v is inside the minimum age %s", now.Sub(c), minAge)
	}
	// A tiny unused window is clamped to the floor: the AGE rule never
	// reaches into it, whatever the minimum age.
	c, _ = goCacheCutoff(mk(5*time.Hour), 1, now, time.Minute, 1<<40, minAge)
	if c.After(now.Add(-goCacheFloor)) {
		t.Fatalf("age cutoff %v is inside the floor", now.Sub(c))
	}
	// A minimum age past the floor is clamped to it.
	c, _ = goCacheCutoff(mk(time.Minute), 1, now, 48*time.Hour, 1, 10*time.Hour)
	if c.After(now.Add(-goCacheFloor)) {
		t.Fatalf("cutoff %v is inside the floor with a 10h minimum age", now.Sub(c))
	}
	// Under budget: only the age rule applies.
	c, est := goCacheCutoff(mk(3*time.Hour, 60*time.Hour), 1, now, 48*time.Hour, 1<<40, minAge)
	if est != 200 || now.Sub(c) < 47*time.Hour {
		t.Fatalf("under budget must use the age rule: est=%d cutoff age=%v", est, now.Sub(c))
	}
	// Over budget: the cutoff reaches into entries younger than the age rule,
	// down to the low-water mark (80 of 100 B budget: 220 B must go, i.e. all
	// three entries here).
	c, _ = goCacheCutoff(mk(3*time.Hour, 5*time.Hour, 10*time.Hour), 1, now, 48*time.Hour, 100, minAge)
	if now.Sub(c) > 3*time.Hour || now.Sub(c) < minAge {
		t.Fatalf("budget cutoff age %v out of range", now.Sub(c))
	}
	// ... and inside the 2h floor when that is what the budget takes (the
	// 2026-10-22 machine: every entry under 4h old).
	c, _ = goCacheCutoff(mk(30*time.Minute, 45*time.Minute, 90*time.Minute, 150*time.Minute), 1, now, 48*time.Hour, 200, minAge)
	if now.Sub(c) >= goCacheFloor || now.Sub(c) < minAge {
		t.Fatalf("over budget with only young entries: cutoff age %v, want inside (%s, %s)", now.Sub(c), minAge, goCacheFloor)
	}
}

// A deadline that hits mid-pass must still have deleted the old entries of the
// shards it covered. Before streaming, the pass held every victim in memory
// until a complete scan finished, so a cut-off pass deleted nothing.
func TestGoCacheCutOffPassStillFreesCoveredShards(t *testing.T) {
	root := t.TempDir()
	fillShards(t, root, 64, 20, func(int, int) time.Duration { return 100 * time.Hour })
	shards, _ := goCacheShardList(root)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel as soon as the first deletions have happened.
	go func() {
		for {
			if countEntries(root) < 64*20 {
				cancel()
				return
			}
			time.Sleep(time.Microsecond)
		}
	}()
	pass := streamTrimGoCache(ctx, root, shards, 0, time.Now().Add(-goCacheFloor), true, unpacedDeleter(nil))
	if pass.removed == 0 || countEntries(root) >= 64*20 {
		t.Fatalf("cut-off pass freed nothing: %+v", pass)
	}
	if pass.removed != 64*20-countEntries(root) {
		t.Fatalf("accounting mismatch: removed=%d remaining=%d", pass.removed, countEntries(root))
	}
}

func countEntries(root string) int {
	n := 0
	subs, _ := os.ReadDir(root)
	for _, s := range subs {
		if s.IsDir() {
			f, _ := os.ReadDir(filepath.Join(root, s.Name()))
			n += len(f)
		}
	}
	return n
}

func TestGoCacheCursorAdvancesAcrossPasses(t *testing.T) {
	root := t.TempDir()
	fillShards(t, root, 8, 4, func(int, int) time.Duration { return 100 * time.Hour })
	policy := filepath.Join(t.TempDir(), "storage.json")
	var out strings.Builder
	r := goRunner(t, &out)
	r.PolicyPath = policy
	r.GoCacheRoot = root
	if r.loadGoCacheCursor("shared go build cache") != 0 {
		t.Fatal("cursor must start at 0")
	}
	// A pass cut off after covering shards 0..2 resumes at 3.
	r.saveGoCacheCursor("shared go build cache", 3)
	if got := r.loadGoCacheCursor("shared go build cache"); got != 3 {
		t.Fatalf("cursor = %d", got)
	}
	if got := r.loadGoCacheCursor("golangci-lint cache"); got != 0 {
		t.Fatalf("cursors must be per cache, got %d", got)
	}
	// A completed apply pass over 8 shards from 3 wraps the cursor back to 3.
	if err := r.SharedGoCache(true); err != nil {
		t.Fatal(err)
	}
	if got := r.loadGoCacheCursor("shared go build cache"); got != 3 {
		t.Fatalf("full rotation should return to 3, got %d", got)
	}
	// Dry-run never moves the cursor.
	r.saveGoCacheCursor("shared go build cache", 5)
	_ = r.SharedGoCache(false)
	if got := r.loadGoCacheCursor("shared go build cache"); got != 5 {
		t.Fatalf("dry-run moved the cursor to %d", got)
	}
}

func TestGoCachePartialPassIsNotAFailure(t *testing.T) {
	root := t.TempDir()
	fillShards(t, root, 32, 10, func(int, int) time.Duration { return 100 * time.Hour })
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.GolangciCacheRoot = t.TempDir()
	r.GoimportsRoot = t.TempDir()
	r.OpenPaths = func(context.Context) (openfiles.Snapshot, error) { return openfiles.FromPaths(nil), nil }
	r.ProcessEnv = func(context.Context) (string, error) { return "x", nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // deadline already passed
	r.Ctx = ctx
	// A cut-off is not a failure, but it is never silent either: the record
	// names every step the deadline stopped, so a scheduler sees the trim did
	// not finish instead of a clean pass.
	err := r.GoCaches(true)
	if RealFailure(err) != nil {
		t.Fatalf("a cut-off pass must not be a failure: %v", err)
	}
	res := NewGCResult(time.Now(), err)
	if !res.OK || !slices.Contains(res.CutOffLayers, "go caches: shared build cache") || len(res.CutOffLayers) != 4 {
		t.Fatalf("every step the deadline stopped must be recorded as cut off: %+v", res)
	}
	// And a live pass over a real cache that finishes reports no error and says so.
	r.Ctx = context.Background()
	out.Reset()
	if err := r.GoCaches(true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(finished)") || countEntries(root) != 0 {
		t.Fatalf("expected a finished pass:\n%s", out.String())
	}
}

// TestGoCacheTrimStoppedByItsDeadlineIsACutOff is the 2026-10-22 machine: under
// load the shared cache's walk outran its slice of the pass, the trim returned
// nil, and every record said the pass finished while ~46 GiB of entries past
// the 2h floor stayed on disk. A walk the deadline stops is a cut-off, with
// how far it got, and the next pass resumes where it stopped.
func TestGoCacheTrimStoppedByItsDeadlineIsACutOff(t *testing.T) {
	root := t.TempDir()
	fillShards(t, root, 32, 10, func(int, int) time.Duration { return 100 * time.Hour })
	policy := filepath.Join(t.TempDir(), "storage.json")
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.PolicyPath = policy
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Ctx = ctx
	removed := 0
	r.governor = newDeleter(Policy{DeleteRatePerSec: 1_000_000_000}, func(p string) error {
		removed++
		if removed == 25 {
			cancel() // the slice ends mid-walk
		}
		return os.Remove(p)
	}, nil)
	var trims []GoCacheTrim
	r.OnGoCacheTrim = func(tr GoCacheTrim) { trims = append(trims, tr) }

	err := r.SharedGoCache(true)
	var co *CutOffError
	if !errors.As(err, &co) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a trim its deadline stopped must return a cut-off, got %v\n%s", err, out.String())
	}
	if len(trims) != 1 || trims[0].Finished || trims[0].ShardsCovered >= trims[0].ShardsTotal || trims[0].Removed == 0 {
		t.Fatalf("the trim's report must say how far it got: %+v", trims)
	}
	if got := r.loadGoCacheCursor("shared go build cache"); got != trims[0].ShardsCovered {
		t.Fatalf("next pass must resume at shard %d, cursor %d", trims[0].ShardsCovered, got)
	}
	res := NewGCResult(time.Now(), layerErr("go caches: shared build cache", err))
	if !res.OK || strings.Join(res.CutOffLayers, ",") != "go caches: shared build cache" {
		t.Fatalf("recorded as %+v", res)
	}
}

// TestGoCacheGCGivesTheGoCachesTheWholeBudget: the Go-cache pass runs the
// Go-cache layer alone — no Docker, no temp sweep — under its own deadline, and
// a pass that reaches every shard finishes.
func TestGoCacheGCGivesTheGoCachesTheWholeBudget(t *testing.T) {
	root := t.TempDir()
	fillShards(t, root, 16, 5, func(int, int) time.Duration { return 100 * time.Hour })
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.GolangciCacheRoot = t.TempDir()
	makeBuildCache(t, r.GolangciCacheRoot)
	putEntry(t, r.GolangciCacheRoot, "ab", hexName(1, "-a"), 10, 100*time.Hour)
	r.GoimportsRoot = t.TempDir()
	r.OpenPaths = func(context.Context) (openfiles.Snapshot, error) { return openfiles.FromPaths(nil), nil }
	r.ProcessEnv = func(context.Context) (string, error) { return "x", nil }
	r.Command = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		t.Errorf("the Go-cache pass ran %s; it must not touch Docker or anything outside the Go caches", name)
		return nil, errors.New("refused")
	}
	var trims []GoCacheTrim
	r.OnGoCacheTrim = func(tr GoCacheTrim) { trims = append(trims, tr) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.GoCacheGC(ctx, true); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if countEntries(root) != 0 || len(trims) != 2 || !trims[0].Finished {
		t.Fatalf("remaining=%d trims=%+v\n%s", countEntries(root), trims, out.String())
	}
}

// TestGoCacheTrimReportsWhatItMeasured: the report a scheduler reads — size,
// budget, the part inside the floor, what went, and that the walk finished.
func TestGoCacheTrimReportsWhatItMeasured(t *testing.T) {
	// 16 shards x (30 young + 10 old) x 100B against a 40,000B budget: the
	// young part alone is over it, so the cutoff is the minimum age.
	r, root, out := overBudgetCache(t, 30, 10)
	var trims []GoCacheTrim
	r.OnGoCacheTrim = func(tr GoCacheTrim) { trims = append(trims, tr) }
	if err := RealFailure(r.SharedGoCache(true)); err != nil {
		t.Fatal(err)
	}
	if len(trims) != 1 {
		t.Fatalf("want one report, got %+v\n%s", trims, out)
	}
	tr := trims[0]
	if tr.Cache != "shared go build cache" || tr.Root != root || !tr.Applied || !tr.Finished ||
		tr.ShardsCovered != 16 || tr.ShardsTotal != 16 || tr.Removed != 16*10 || tr.FreedBytes != 16*10*100 ||
		tr.EstimatedBytes != 16*40*100 || tr.YoungBytes != 16*30*100 || tr.BudgetBytes != 40_000 {
		t.Fatalf("report = %+v\n%s", tr, out)
	}
	if !tr.Cutoff.Before(time.Now().Add(-defaultGoCacheMinAge + time.Minute)) {
		t.Fatalf("cutoff %s is inside the minimum age", tr.Cutoff)
	}
}

func TestGoCacheTrimHonoursBudgetViaSample(t *testing.T) {
	root := t.TempDir()
	// 64 shards x 10 entries x 100B = 64000B, ages spread 3h..~200h by index.
	fillShards(t, root, 64, 10, func(s, i int) time.Duration { return time.Duration(3+i*20) * time.Hour })
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.Policy.GoCacheGiB = 1 // budget is huge vs 64KB: only the 48h age rule applies
	if err := r.SharedGoCache(true); err != nil {
		t.Fatal(err)
	}
	if got := countEntries(root); got != 64*3 { // ages 3h, 23h, 43h kept; 63h+ removed
		t.Fatalf("age rule: %d entries left, want %d\n%s", got, 64*3, out.String())
	}
}

// TestGoCacheOverBudgetIsNotStoppedByTheCap is the 2026-10-18 pass: 62 GiB
// estimated against a 20 GiB budget, 6.1 GiB freed, "stopped at this pass's
// delete cap". A cache over its size budget trims to the budget in one pass,
// least recently used first; the cap still bounds the age rule.
func TestGoCacheOverBudgetIsNotStoppedByTheCap(t *testing.T) {
	root := t.TempDir()
	// 16 shards x 50 entries x 100 B = 80 000 B; entry i is (3+i)h old.
	fillShards(t, root, 16, 50, func(_, i int) time.Duration { return time.Duration(3+i) * time.Hour })
	prev := goCacheDiskSpace
	goCacheDiskSpace = func(path string) (Disk, error) { return Disk{Path: path, Capacity: 4_000_000}, nil }
	t.Cleanup(func() { goCacheDiskSpace = prev })
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.Policy.GoCacheVolumePercent = 1 // budget: 1% of 4 MB = 40 000 B, half the cache
	r.Policy.MaxDeletesPerPass = 50
	r.Policy.DeleteRatePerSec = 1_000_000_000

	if err := r.SharedGoCache(true); err != nil {
		t.Fatalf("over-budget pass: %v\n%s", err, out.String())
	}
	if got := countEntries(root); got != 320 {
		t.Fatalf("%d entries left, want 320 (the low-water mark, 80%% of the budget), not 800 minus a 50-entry cap\n%s", got, out.String())
	}
	// Least recently used first: entries 0..19 (3h..22h) stay, 20..49 go.
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == "README" {
			return nil
		}
		info, _ := d.Info()
		if age := time.Since(info.ModTime()); age > 22*time.Hour+time.Minute {
			t.Errorf("%s (%s old) survived while newer entries went", filepath.Base(p), age.Round(time.Hour))
		}
		return nil
	})
	if !strings.Contains(out.String(), "per-pass cap lifted") {
		t.Fatalf("the pass must say it lifted the cap:\n%s", out.String())
	}
}

// TestGoCacheAgeRuleStaysCapped: under budget, the cap holds as before.
func TestGoCacheAgeRuleStaysCapped(t *testing.T) {
	root := t.TempDir()
	fillShards(t, root, 8, 25, func(int, int) time.Duration { return 100 * time.Hour })
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.Policy.MaxDeletesPerPass = 50
	r.Policy.DeleteRatePerSec = 1_000_000_000
	_ = r.SharedGoCache(true)
	if got := countEntries(root); got != 200-50 {
		t.Fatalf("%d entries left, want the age rule capped at 50 unlinks\n%s", got, out.String())
	}
}

func TestGoCacheBudgetIsCappedByTheVolume(t *testing.T) {
	prev := goCacheDiskSpace
	t.Cleanup(func() { goCacheDiskSpace = prev })
	for _, tc := range []struct {
		name     string
		capacity uint64
		err      error
		percent  int
		want     uint64
	}{
		{"40 GiB build volume: 15% of it", 40 * GiB, nil, 0, 40 * GiB / 100 * 15},
		{"1 TiB volume: go_cache_gib", 1024 * GiB, nil, 0, 60 * GiB},
		{"100 percent leaves go_cache_gib", 40 * GiB, nil, 100, 60 * GiB},
		{"unreadable volume leaves go_cache_gib", 0, os.ErrPermission, 0, 60 * GiB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goCacheDiskSpace = func(path string) (Disk, error) { return Disk{Path: path, Capacity: tc.capacity}, tc.err }
			r := Runner{Policy: DefaultPolicy()}
			r.Policy.GoCacheVolumePercent = tc.percent
			if got := r.goCacheBudget("/cache"); got != tc.want {
				t.Fatalf("budget = %d, want %d", got, tc.want)
			}
		})
	}
	bad := DefaultPolicy()
	bad.GoCacheVolumePercent = 101
	if bad.Validate() == nil {
		t.Fatal("go_cache_volume_percent 101 must be rejected")
	}
}

// overBudgetCache is a 16-shard cache with a 40 000 B budget (1% of a stubbed
// 4 MB volume): in every shard, `young` 100 B entries 1 minute old (inside the
// minimum age) and `old` ones 5 hours old.
func overBudgetCache(t *testing.T, young, old int) (Runner, string, *strings.Builder) {
	t.Helper()
	root := t.TempDir()
	fillShards(t, root, 16, young+old, func(_, i int) time.Duration {
		if i < young {
			return time.Minute
		}
		return 5 * time.Hour
	})
	prev := goCacheDiskSpace
	goCacheDiskSpace = func(path string) (Disk, error) { return Disk{Path: path, Capacity: 4_000_000}, nil }
	t.Cleanup(func() { goCacheDiskSpace = prev })
	out := &strings.Builder{}
	r := goRunner(t, out)
	r.GoCacheRoot = root
	r.Policy.GoCacheVolumePercent = 1
	r.Policy.DeleteRatePerSec = 1_000_000_000
	return r, root, out
}

// TestGoCacheBudgetUnreachableInsideTheFloor is the 2026-10-21 machine: a
// 20 GiB budget, a 152 GiB cache, and ~140 GiB of it written in the last two
// hours. Since the size rule may reach into the floor this needs the fill to
// be inside the MINIMUM AGE (20m) to be out of reach: no pass can delete
// entries that young — a build may be writing them — so the budget cannot be
// met, and every pass used to report itself finished. The pass must still
// remove everything older, never anything younger, and say plainly that the
// budget is out of reach and why.
func TestGoCacheBudgetUnreachableInsideTheFloor(t *testing.T) {
	// 48 000 B young against a 40 000 B budget, plus 16 000 B old.
	r, root, out := overBudgetCache(t, 30, 10)

	err := r.SharedGoCache(true)
	var ub *BudgetUnreachableError
	if !errors.As(err, &ub) {
		t.Fatalf("want a BudgetUnreachableError, got %v\n%s", err, out)
	}
	if ub.Young != 48_000 || ub.Budget != 40_000 || ub.Floor != defaultGoCacheMinAge {
		t.Fatalf("young=%d budget=%d floor=%s, want 48000, 40000, %s", ub.Young, ub.Budget, ub.Floor, defaultGoCacheMinAge)
	}
	if got := countEntries(root); got != 16*30 {
		t.Fatalf("%d entries left, want the %d young ones: everything past the floor goes, nothing inside it\n%s", got, 16*30, out)
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == "README" {
			return nil
		}
		if info, _ := d.Info(); time.Since(info.ModTime()) > defaultGoCacheMinAge {
			t.Errorf("%s is past the minimum age and survived an over-budget pass", filepath.Base(p))
		}
		return nil
	})
	for _, want := range []string{"WARNING", "no trim can reach it", "-trimpath", "go_cache_gib"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the pass output does not say %q:\n%s", want, out)
		}
	}

	// It is a finding, not a failure: the record says over budget, the pass OK.
	res := NewGCResult(time.Now(), layerErr("go caches: shared build cache", err))
	if !res.OK || len(res.FailedLayers) != 0 || len(res.OverBudget) != 1 || !strings.Contains(res.OverBudget[0], "shared go build cache") {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Summary(), "over a budget no trim can reach") {
		t.Fatalf("summary hides the unreachable budget: %q", res.Summary())
	}
	if RealFailure(err) != nil {
		t.Fatalf("an unreachable budget must not fail the pass: %v", RealFailure(err))
	}
}

// A budget the old entries alone can satisfy is reachable: no finding.
func TestGoCacheBudgetReachableIsSilent(t *testing.T) {
	// 16 000 B young under a 40 000 B budget, plus 48 000 B old.
	r, root, out := overBudgetCache(t, 10, 30)
	if err := r.SharedGoCache(true); err != nil {
		t.Fatalf("a reachable budget reported %v\n%s", err, out)
	}
	if got := countEntries(root); got > 16*20 || got < 16*10 {
		t.Fatalf("%d entries left, want the budget's worth (160..320)\n%s", got, out)
	}
	if strings.Contains(out.String(), "WARNING") {
		t.Fatalf("a reachable budget warned:\n%s", out)
	}
}

// Through the whole Go-cache layer, as auto-gc runs it: a pass that is also
// cut off records both, and neither is a failure.
func TestGoCachesRecordsCutOffAndUnreachableTogether(t *testing.T) {
	r, _, out := overBudgetCache(t, 30, 10)
	r.GolangciCacheRoot = t.TempDir()
	r.GoimportsRoot = t.TempDir()
	r.OpenPaths = func(context.Context) (openfiles.Snapshot, error) { return openfiles.FromPaths(nil), nil }
	r.ProcessEnv = func(context.Context) (string, error) { return "x", nil }
	err := r.GoCaches(true)
	res := NewGCResult(time.Now(), errors.Join(err, layerErr("temp sweep", &CutOffError{Err: context.DeadlineExceeded})))
	if !res.OK || len(res.OverBudget) != 1 || strings.Join(res.CutOffLayers, ",") != "temp sweep" {
		t.Fatalf("result = %+v (err %v)\n%s", res, err, out)
	}
	both := NewGCResult(time.Now(), layerErr("go caches: shared build cache",
		errors.Join(&CutOffError{Err: errDeleteCap}, &BudgetUnreachableError{Cache: "c", Floor: defaultGoCacheMinAge})))
	if !both.OK || len(both.OverBudget) != 1 || strings.Join(both.CutOffLayers, ",") != "go caches: shared build cache" {
		t.Fatalf("one layer ending both ways must record both: %+v", both)
	}
}

func TestGoCacheStatusReportsTheYoungShare(t *testing.T) {
	r, _, out := overBudgetCache(t, 30, 10)
	r.goCacheStatus(context.Background())
	for _, want := range []string{"~0.0 GiB used in the last 20m0s", "WARNING"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status does not say %q:\n%s", want, out)
		}
	}
}

// TestGoCacheOverBudgetReachesIntoTheFloor is the 2026-10-22 machine: a 20 GiB
// budget, a 74 GB cache, every entry used in the last 4h. The age rule cannot
// touch anything in the 2h floor, and the old size rule stopped there too, so
// the budget never held. Over budget, the size rule now evicts least recently
// used first down to the low-water mark, as deep into the window as it must
// and no deeper than the minimum age.
func TestGoCacheOverBudgetReachesIntoTheFloor(t *testing.T) {
	root := t.TempDir()
	// 16 shards x 60 entries x 100 B = 96 000 B; entry i is (5+i)m old, so
	// 0..95m: everything is inside the 2h floor.
	fillShards(t, root, 16, 60, func(_, i int) time.Duration { return time.Duration(5+i) * time.Minute })
	prev := goCacheDiskSpace
	goCacheDiskSpace = func(path string) (Disk, error) { return Disk{Path: path, Capacity: 8_000_000}, nil }
	t.Cleanup(func() { goCacheDiskSpace = prev })
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.Policy.GoCacheVolumePercent = 1 // 80 000 B budget; low-water 64 000 B
	r.Policy.DeleteRatePerSec = 1_000_000_000

	if err := r.SharedGoCache(false); err != nil || countEntries(root) != 960 {
		t.Fatalf("dry-run: err=%v entries=%d, want 960 untouched\n%s", err, countEntries(root), out.String())
	}
	if !strings.Contains(out.String(), "would remove") {
		t.Fatalf("dry-run must say what it would remove:\n%s", out.String())
	}
	if err := r.SharedGoCache(true); err != nil {
		t.Fatalf("over-budget pass inside the floor: %v\n%s", err, out.String())
	}
	got := countEntries(root)
	if got < 560 || got > 720 { // 640 target, sample error allowed
		t.Fatalf("%d entries left, want ~640 (80%% of the budget)\n%s", got, out.String())
	}
	// LRU: what survives is the newest; nothing newer than the minimum age
	// was needed, and the survivors are younger than anything removed.
	var oldestKept time.Duration
	var youngest = time.Hour
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == "README" {
			return nil
		}
		info, _ := d.Info()
		age := time.Since(info.ModTime())
		oldestKept = max(oldestKept, age)
		youngest = min(youngest, age)
		return nil
	})
	if oldestKept > 80*time.Minute {
		t.Fatalf("an entry %s old survived while the pass was over budget", oldestKept.Round(time.Minute))
	}
	if youngest > 6*time.Minute {
		t.Fatalf("the newest entries were evicted (youngest left %s)", youngest.Round(time.Minute))
	}
}

// The minimum age is a hard stop: entries newer than it are never evicted,
// and go_cache_min_age is the knob. At 2h the old behaviour is back.
func TestGoCacheMinAgeIsAHardStop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		minAge  string
		wantMin int // entries that must survive: the ones newer than minAge
	}{
		{"default 20m", "", 16 * 15},
		{"explicit 90m", "90m", 16 * 85},
		{"2h restores the floor", "2h", 16 * 115},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// entry i is (5+i)m old, 120 per shard: 5m..124m.
			fillShards(t, root, 16, 120, func(_, i int) time.Duration { return time.Duration(5+i) * time.Minute })
			prev := goCacheDiskSpace
			goCacheDiskSpace = func(path string) (Disk, error) { return Disk{Path: path, Capacity: 100_000}, nil }
			t.Cleanup(func() { goCacheDiskSpace = prev })
			var out strings.Builder
			r := goRunner(t, &out)
			r.GoCacheRoot = root
			r.Policy.GoCacheVolumePercent = 1 // 1 000 B: nothing short of the stop satisfies it
			r.Policy.GoCacheMinAge = tc.minAge
			r.Policy.DeleteRatePerSec = 1_000_000_000
			r.Policy.MaxDeletesPerPass = 1_000_000
			err := r.SharedGoCache(true)
			var ub *BudgetUnreachableError
			if !errors.As(err, &ub) {
				t.Fatalf("want BudgetUnreachableError, got %v\n%s", err, out.String())
			}
			if got := countEntries(root); got < tc.wantMin {
				t.Fatalf("%d entries left, want at least the %d younger than the minimum age\n%s", got, tc.wantMin, out.String())
			}
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || filepath.Base(p) == "README" {
					return nil
				}
				info, _ := d.Info()
				limit := defaultGoCacheMinAge
				if tc.minAge != "" {
					limit, _ = time.ParseDuration(tc.minAge)
				}
				if age := time.Since(info.ModTime()); age > limit+10*time.Minute {
					t.Errorf("%s (%s old) survived: nothing older than the minimum age should", filepath.Base(p), age.Round(time.Minute))
				}
				return nil
			})
		})
	}
}

// An executable entry (`go run`, `go tool`) is a directory holding the binary
// and is the largest kind of entry. It counts toward the size and is evicted
// whole, through the governor.
func TestGoCacheExecutableEntriesAreMeasuredAndEvicted(t *testing.T) {
	root := t.TempDir()
	makeBuildCache(t, root)
	const bin = 5000
	execDir := func(shard, name string, age time.Duration) string {
		dir := filepath.Join(root, shard, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cli"), make([]byte, bin), 0o755); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	oldExec := execDir("aa", hexName(1, "-d"), 3*time.Hour)
	newExec := execDir("bb", hexName(2, "-d"), time.Minute)
	small := putEntry(t, root, "cc", hexName(3, "-a"), 10, 3*time.Hour)

	entries, err := readGoCacheShard(root, "aa")
	if err != nil || len(entries) != 1 || !entries[0].dir || entries[0].size != bin {
		t.Fatalf("executable entry not measured as a %d-byte directory: %+v (%v)", bin, entries, err)
	}

	prev := goCacheDiskSpace
	goCacheDiskSpace = func(path string) (Disk, error) { return Disk{Path: path, Capacity: 1_000_000}, nil }
	t.Cleanup(func() { goCacheDiskSpace = prev })
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot = root
	r.Policy.GoCacheVolumePercent = 1 // 10 000 B budget; the three entries are ~10 010 B
	r.Policy.DeleteRatePerSec = 1_000_000_000
	if err := r.SharedGoCache(true); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if exists(oldExec) || exists(small) || !exists(newExec) {
		t.Fatalf("oldExec=%v small=%v newExec=%v\n%s", exists(oldExec), exists(small), exists(newExec), out.String())
	}
}

func TestGoCacheMinAgeValidation(t *testing.T) {
	for v, ok := range map[string]bool{"": true, "5m": true, "20m": true, "2h": true, "4m": false, "3h": false, "soon": false} {
		p := DefaultPolicy()
		p.GoCacheMinAge = v
		if err := p.Validate(); (err == nil) != ok {
			t.Errorf("go_cache_min_age %q: err = %v, want ok=%v", v, err, ok)
		}
	}
}
