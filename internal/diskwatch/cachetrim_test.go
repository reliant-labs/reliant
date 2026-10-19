// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// trimMachine is a watcher over real temp directories for the npm cache, the
// module cache and TMPDIR, on a fake volume of the given size.
type trimMachine struct {
	w                  *Watcher
	now                time.Time
	npm, gomod, tmpdir string
}

func newTrimMachine(t *testing.T, volume uint64, cfg CacheTrimConfig) *trimMachine {
	t.Helper()
	root := t.TempDir()
	m := &trimMachine{now: time.Now(), npm: filepath.Join(root, "npm"), gomod: filepath.Join(root, "gomod"), tmpdir: filepath.Join(root, "tmp")}
	home := filepath.Join(root, "home")
	for _, d := range []string{m.npm, m.gomod, m.tmpdir, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const bsize = 4096
	fsys := &fakeFS{
		mounts: map[string]fsStat{root: {Dev: 9, Bsize: bsize, Blocks: volume / bsize, Bfree: volume / bsize / 2, Bavail: volume / bsize / 2, Files: 1 << 20, Ffree: 1 << 19}},
		exists: map[string]bool{root: true, m.npm: true, m.gomod: true, m.tmpdir: true, home: true},
	}
	envm := map[string]string{"npm_config_cache": m.npm, "GOMODCACHE": m.gomod, "GOCACHE": filepath.Join(root, "nogocache"), "TMPDIR": m.tmpdir}
	env := Environment{Getenv: func(k string) string { return envm[k] }, Home: home, GOOS: "test"}
	m.w = watcherOn(t, fsys, env, &m.now)
	cfg.RatePerSec = 1 << 20
	m.w.ConfigureCacheTrim(cfg)
	return m
}

// npmEntry writes one npm content file in shard i, last used age ago.
func npmEntry(t *testing.T, npm string, i int, size int, age time.Duration, now time.Time) string {
	t.Helper()
	p := filepath.Join(npm, "_cacache", "content-v2", "sha512", fmt.Sprintf("%02x", i%16), fmt.Sprintf("%02x", i), fmt.Sprintf("entry%03d", i))
	writeFile(t, p, size, now.Add(-age))
	return p
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func allocated(t *testing.T, p string) uint64 {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return allocatedBytes(info)
}

func TestCacheBudgetIsDerivedFromTheVolume(t *testing.T) {
	d := DefaultCacheTrimConfig()
	for _, tc := range []struct {
		name             string
		volume           uint64
		wantNpm, wantMod uint64
	}{
		{"413 GiB agent machine", 413 * GiB, 413 * GiB * 2 / 100, 413 * GiB / 10},
		{"40 GiB cache volume takes the floors", 40 * GiB, 2 * GiB, 10 * GiB},
		{"4 TiB volume takes the ceilings", 4096 * GiB, 10 * GiB, 60 * GiB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Npm.bytesFor(tc.volume); got != tc.wantNpm {
				t.Errorf("npm budget = %s, want %s", FormatBytes(got), FormatBytes(tc.wantNpm))
			}
			if got := d.GoModCeiling.bytesFor(tc.volume); got != tc.wantMod {
				t.Errorf("module cache ceiling = %s, want %s", FormatBytes(got), FormatBytes(tc.wantMod))
			}
		})
	}
}

func TestCacheTrimConfigZeroValueIsTheDefault(t *testing.T) {
	got := CacheTrimConfig{}.withDefaults()
	if got.Npm != DefaultCacheTrimConfig().Npm || got.GoModCeiling != DefaultCacheTrimConfig().GoModCeiling || got.GoModInterval != 24*time.Hour {
		t.Fatalf("zero config = %+v, want the defaults", got)
	}
	if off := (CacheTrimConfig{Npm: CacheBudget{Off: true}}).withDefaults(); !off.Npm.Off {
		t.Fatal("Off must survive defaulting")
	}
}

// TestTrimNpm_EnforcesBudgetLeastRecentlyUsedFirst: 40 equal entries, one
// every 15 minutes, budget 20 of them, target half the budget: the 30 oldest
// go, the 10 newest stay.
func TestTrimNpm_EnforcesBudgetLeastRecentlyUsedFirst(t *testing.T) {
	const size = 8192
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	var paths []string
	for i := 0; i < 40; i++ {
		paths = append(paths, npmEntry(t, m.npm, i, size, time.Duration(40-i)*15*time.Minute, m.now))
	}
	unit := allocated(t, paths[0])
	m.w.ConfigureCacheTrim(CacheTrimConfig{RatePerSec: 1 << 20, Npm: CacheBudget{Min: 20 * unit, Max: 20 * unit, Target: 0.5, MinAge: time.Hour}})
	// Bookkeeping npm keeps beside the entries is never an eviction unit.
	writeFile(t, filepath.Join(m.npm, "_cacache", "tmp", "inflight"), size, m.now.Add(-48*time.Hour))

	res := m.w.TrimCaches(context.Background(), false)

	if len(res.Caches) == 0 || res.Caches[0].Kind != KindNpmCache {
		t.Fatalf("result = %+v, want an npm trim", res)
	}
	r := res.Caches[0]
	if r.Size != 40*unit || r.Budget != 20*unit || r.Removed != 30 || r.Stopped != "" {
		t.Fatalf("npm trim = %+v, want 30 of 40 removed", r)
	}
	for i, p := range paths {
		if exists := exists(p); exists != (i >= 30) {
			t.Errorf("entry %d (age %s): exists=%v, want only the 10 most recently used kept", i, time.Duration(40-i)*15*time.Minute, exists)
		}
	}
	if !exists(filepath.Join(m.npm, "_cacache", "tmp", "inflight")) {
		t.Error("npm's tmp dir was trimmed")
	}
}

// TestTrimNpm_AccessTimeCountsAsUse: npm writes an entry once and only reads
// it after, so a read (atime) is what makes an old entry recently used.
func TestTrimNpm_AccessTimeCountsAsUse(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	var paths []string
	for i := 0; i < 4; i++ {
		paths = append(paths, npmEntry(t, m.npm, i, 8192, 30*24*time.Hour, m.now))
	}
	readYesterday := m.now.Add(-2 * time.Hour)
	if err := os.Chtimes(paths[0], readYesterday, m.now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	unit := allocated(t, paths[1])
	m.w.ConfigureCacheTrim(CacheTrimConfig{RatePerSec: 1 << 20, Npm: CacheBudget{Min: 2 * unit, Max: 2 * unit, Target: 0.5, MinAge: time.Hour}})

	m.w.TrimCaches(context.Background(), false)

	if !exists(paths[0]) {
		t.Fatal("an entry read 2h ago was evicted before entries untouched for 30 days")
	}
	for _, p := range paths[1:] {
		if exists(p) {
			t.Errorf("%s untouched for 30 days survived", filepath.Base(p))
		}
	}
}

func TestTrimNpm_NeverRemovesWithinMinAge(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	old := npmEntry(t, m.npm, 1, 8192, 48*time.Hour, m.now)
	fresh := npmEntry(t, m.npm, 2, 8192, 2*time.Hour, m.now)
	m.w.ConfigureCacheTrim(CacheTrimConfig{RatePerSec: 1 << 20, Npm: CacheBudget{Min: 1, Max: 1, Target: 0.5, MinAge: 24 * time.Hour}})

	res := m.w.TrimCaches(context.Background(), false)

	if exists(old) || !exists(fresh) {
		t.Fatalf("old exists=%v fresh exists=%v; want only the entry past MinAge removed", exists(old), exists(fresh))
	}
	if r := res.Caches[0]; !strings.Contains(r.Stopped, "nothing else unused") {
		t.Fatalf("Stopped = %q, want it to say the rest is too recent", r.Stopped)
	}
}

func TestTrimNpm_WithinBudgetRemovesNothing(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{}) // default budget: 2 GiB
	p := npmEntry(t, m.npm, 1, 8192, 90*24*time.Hour, m.now)
	res := m.w.TrimCaches(context.Background(), false)
	if !exists(p) || res.Caches[0].Removed != 0 {
		t.Fatalf("a cache inside its budget was trimmed: %+v", res.Caches)
	}
}

func TestTrimNpm_StopsAtFilesystemDistress(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	for i := 0; i < 8; i++ {
		npmEntry(t, m.npm, i, 8192, 48*time.Hour, m.now)
	}
	m.w.ConfigureCacheTrim(CacheTrimConfig{RatePerSec: 1 << 20, Npm: CacheBudget{Min: 1, Max: 1, Target: 0.5, MinAge: time.Hour}})
	var calls atomic.Int32
	m.w.trim.remove = func(string) error { calls.Add(1); return syscall.EIO }

	res := m.w.TrimCaches(context.Background(), false)

	if calls.Load() != 1 {
		t.Fatalf("%d unlinks after the first EIO, want the pass to stop at once", calls.Load())
	}
	if r := res.Caches[0]; r.Removed != 0 || !strings.Contains(r.Stopped, "distress") {
		t.Fatalf("npm trim = %+v, want a distress stop", r)
	}
}

// modVersionFixture writes a module version the way the go command lays it
// out: download files under cache/download and a read-only extracted tree.
func modVersionFixture(t *testing.T, root, mod, ver string, size int, used time.Time) (tree string, files []string) {
	t.Helper()
	dl := filepath.Join(root, "cache", "download", mod, "@v")
	for _, ext := range []string{".info", ".mod", ".zip", ".ziphash"} {
		p := filepath.Join(dl, ver+ext)
		writeFile(t, p, size, used)
		files = append(files, p)
	}
	tree = filepath.Join(root, mod+"@"+ver)
	writeFile(t, filepath.Join(tree, "go.mod"), size, used)
	writeFile(t, filepath.Join(tree, "pkg", "x.go"), size, used)
	for _, p := range []string{filepath.Join(tree, "go.mod"), filepath.Join(tree, "pkg", "x.go")} {
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{filepath.Join(tree, "pkg"), tree} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, used, used); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { // read-only trees outlive t.TempDir's RemoveAll otherwise
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	return tree, files
}

func TestTrimGoMod_UntouchedBelowItsCeiling(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{}) // ceiling 10 GiB
	tree, _ := modVersionFixture(t, m.gomod, "example.com/old", "v1.0.0", 8192, m.now.Add(-365*24*time.Hour))
	res := m.w.TrimCaches(context.Background(), false)
	if !exists(tree) {
		t.Fatal("the module cache was trimmed below its ceiling")
	}
	for _, r := range res.Caches {
		if r.Kind == KindGoModCache && r.Removed != 0 {
			t.Fatalf("module trim = %+v", r)
		}
	}
}

func TestTrimGoMod_AboveCeilingEvictsWholeOldVersionsOnly(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	oldTree, oldFiles := modVersionFixture(t, m.gomod, "example.com/old", "v1.0.0", 8192, m.now.Add(-30*24*time.Hour))
	newTree, newFiles := modVersionFixture(t, m.gomod, "example.com/new", "v2.0.0", 8192, m.now.Add(-24*time.Hour))
	m.w.ConfigureCacheTrim(CacheTrimConfig{RatePerSec: 1 << 20, GoModCeiling: CacheBudget{Min: 1, Max: 1, Target: 0.5, MinAge: 7 * 24 * time.Hour}})

	res := m.w.TrimCaches(context.Background(), false)

	if exists(oldTree) {
		t.Fatal("old module version's tree survived above the ceiling")
	}
	for _, f := range oldFiles {
		if exists(f) {
			t.Errorf("old download file %s survived", filepath.Base(f))
		}
	}
	if !exists(newTree) || !exists(newFiles[0]) {
		t.Fatal("a module version used yesterday was evicted (MinAge 7 days)")
	}
	var r CacheTrimResult
	for _, c := range res.Caches {
		if c.Kind == KindGoModCache {
			r = c
		}
	}
	if r.Removed != 1 {
		t.Fatalf("module trim = %+v, want one version removed", r)
	}

	// Measured once per GoModInterval: the next scheduled pass skips it.
	modVersionFixture(t, m.gomod, "example.com/older", "v0.1.0", 8192, m.now.Add(-60*24*time.Hour))
	again := m.w.trimNativeCaches(context.Background(), false, false, "test")
	for _, c := range again {
		if c.Kind == KindGoModCache {
			t.Fatalf("module cache re-measured inside GoModInterval: %+v", c)
		}
	}
}

func TestTrimGoWorkDirs_RemovesOnlyStaleOnes(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	stale := filepath.Join(m.tmpdir, "go-build1234")
	live := filepath.Join(m.tmpdir, "go-build5678")
	other := filepath.Join(m.tmpdir, "not-go")
	old := m.now.Add(-13 * time.Hour)
	writeFile(t, filepath.Join(stale, "b001", "exe", "a.out"), 4096, old)
	writeFile(t, filepath.Join(live, "b001", "importcfg"), 4096, old)
	writeFile(t, filepath.Join(other, "f"), 4096, old)
	for _, d := range []string{filepath.Join(stale, "b001", "exe"), filepath.Join(stale, "b001"), stale, filepath.Join(live, "b001"), other} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// live's own mtime is fresh: a build is still writing into it.

	m.w.TrimCaches(context.Background(), false)

	if exists(stale) || !exists(live) || !exists(other) {
		t.Fatalf("stale=%v live=%v other=%v; want only the stale go-build dir removed", exists(stale), exists(live), exists(other))
	}
}

// TestTrimCaches_UrgentEntrypoint is the disk-pressure guard's call: it trims
// to UrgentFactor of the budget and runs forge's reaper too.
func TestTrimCaches_UrgentEntrypoint(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	var paths []string
	for i := 0; i < 20; i++ {
		paths = append(paths, npmEntry(t, m.npm, i, 8192, time.Duration(20-i)*time.Hour+2*time.Hour, m.now))
	}
	unit := allocated(t, paths[0])
	m.w.ConfigureCacheTrim(CacheTrimConfig{RatePerSec: 1 << 20, UrgentFactor: 0.5,
		Npm: CacheBudget{Min: 16 * unit, Max: 16 * unit, Target: 1, MinAge: time.Hour}})
	var reaperRuns atomic.Int32
	m.w.reaper.run = func(context.Context, string) (reaperRun, error) { reaperRuns.Add(1); return reaperRun{}, nil }

	// Not urgent: 20 entries against a budget of 16 → down to 16.
	if r := m.w.TrimCaches(context.Background(), false).Caches[0]; r.Removed != 4 {
		t.Fatalf("scheduled trim = %+v, want 4 removed", r)
	}
	// Urgent: the budget halves to 8.
	res := m.w.TrimCaches(context.Background(), true)
	if r := res.Caches[0]; r.Budget != 8*unit || r.Removed != 8 {
		t.Fatalf("urgent trim = %+v, want 8 more removed to a budget of 8", r)
	}
	if reaperRuns.Load() != 2 {
		t.Fatalf("reaper ran %d times, want once per TrimCaches", reaperRuns.Load())
	}
	var kept []int
	for i, p := range paths {
		if exists(p) {
			kept = append(kept, i)
		}
	}
	if !sort.IntsAreSorted(kept) || len(kept) != 8 || kept[0] != 12 {
		t.Fatalf("kept entries %v, want the 8 most recently used (12..19)", kept)
	}
}

// TestTrimCaches_SingleFlight: the guard, the schedule and a user's clean-up
// share one storage pass at a time.
func TestTrimCaches_SingleFlight(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	var inFlight, maxInFlight atomic.Int32
	m.w.reaper.run = func(context.Context, string) (reaperRun, error) {
		n := inFlight.Add(1)
		for {
			cur := maxInFlight.Load()
			if n <= cur || maxInFlight.CompareAndSwap(cur, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return reaperRun{}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(urgent bool) {
			defer wg.Done()
			m.w.TrimCaches(context.Background(), urgent)
		}(i%2 == 0)
	}
	wg.Wait()
	if maxInFlight.Load() != 1 {
		t.Fatalf("%d storage passes ran at once, want 1", maxInFlight.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.w.reaper.sem <- struct{}{} // a pass is running
	defer func() { <-m.w.reaper.sem }()
	if res := m.w.TrimCaches(ctx, true); len(res.Caches) != 0 || res.Reaper.Ran {
		t.Fatal("a cancelled caller must give up waiting, not run a second pass")
	}
}

func TestTrimNativeCaches_RefusesTheRealMachineUnderTest(t *testing.T) {
	w := New(Options{noDocker: true, reaper: func(context.Context, string) (reaperRun, error) { return reaperRun{}, nil }})
	if out := w.trimNativeCaches(context.Background(), true, true, "test"); out != nil {
		t.Fatalf("trimmed this machine's real caches from a test: %+v", out)
	}
}
