package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/gocache"
)

// TestDefaultPolicyIsTheGoCachePackagesPolicy: the Go build cache defaults
// live once, in forge/pkg/gocache, which the Reliant daemon reads too. A
// default written here a second time is the drift that package exists to stop.
func TestDefaultPolicyIsTheGoCachePackagesPolicy(t *testing.T) {
	p := DefaultPolicy()
	unused, err := time.ParseDuration(p.GoCacheUnused)
	if err != nil {
		t.Fatal(err)
	}
	if unused != gocache.DefaultUnused || p.GoCacheGiB != gocache.DefaultBudgetGiB || p.GolangciCacheGiB != gocache.DefaultGolangciBudgetGiB {
		t.Fatalf("DefaultPolicy go cache = %s / %d GiB / %d GiB, want pkg/gocache's %s / %d / %d",
			p.GoCacheUnused, p.GoCacheGiB, p.GolangciCacheGiB, gocache.DefaultUnused, gocache.DefaultBudgetGiB, gocache.DefaultGolangciBudgetGiB)
	}
	if p.GoCacheUnused != "24h" {
		t.Fatalf("go_cache_unused is written %q, want the plain 24h", p.GoCacheUnused)
	}
	if goCacheFloor != gocache.Floor || goCacheMinFloor != gocache.MinFloor || goCacheLowWater != gocache.LowWater || defaultGoCacheVolumePercent != gocache.DefaultVolumePercent {
		t.Fatal("the trim's floors, low water or volume cap are not pkg/gocache's")
	}
}

// TestLoadReadsTheOldGoCacheDefaultAsTheNewOne: every policy an older forge
// saved carries go_cache_unused "48h", chosen or not (Save writes every
// field), so the lowered default reaches those machines only if Load reads it
// as the default. Any other value is the machine's choice and is kept.
func TestLoadReadsTheOldGoCacheDefaultAsTheNewOne(t *testing.T) {
	dir := t.TempDir()
	for doc, want := range map[string]string{
		`{"go_cache_unused": "48h"}`:     "24h",
		`{"go_cache_unused": "48h0m0s"}`: "48h0m0s",
		`{"go_cache_unused": "72h"}`:     "72h",
		`{}`:                             "24h",
	} {
		path := filepath.Join(dir, "storage.json")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if p.GoCacheUnused != want {
			t.Errorf("%s: go_cache_unused = %q, want %q", doc, p.GoCacheUnused, want)
		}
	}
}

func TestValidateRefusesARelativeGoCache(t *testing.T) {
	p := DefaultPolicy()
	p.GoCaches = []string{"cache/go-build"}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "go_caches") {
		t.Fatalf("a relative go_caches entry must be refused, got %v", err)
	}
	p.GoCaches = []string{"/srv/gha/cache/forge/go-build"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestAGoCacheThisUserCannotTrimIsAFailure: cicd's runner caches sit under a
// 0700 directory of another user. A declared cache the pass cannot reach, or
// whose entries it cannot remove, is a failed layer naming the cache, never a
// clean pass over nothing.
func TestAGoCacheThisUserCannotTrimIsAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	parent := t.TempDir()
	unreachable := filepath.Join(parent, "locked", "go-build")
	makeBuildCache(t, unreachable)
	readOnly := filepath.Join(parent, "ro", "go-build")
	makeBuildCache(t, readOnly)
	putEntry(t, readOnly, "ab", hexName(1, "-a"), 10, 30*time.Hour)
	for dir, mode := range map[string]os.FileMode{filepath.Join(parent, "locked"): 0o000, filepath.Join(readOnly, "ab"): 0o555} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot, r.GolangciCacheRoot = t.TempDir(), t.TempDir()
	r.Policy.GoCaches = []string{unreachable, readOnly}
	err := RealFailure(r.GoCaches(true))
	if err == nil {
		t.Fatalf("a cache the pass cannot trim must fail its layer:\n%s", out.String())
	}
	for _, want := range []string{"go caches: go build cache " + unreachable, "go caches: go build cache " + readOnly, "could not be removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestGoCachesTrimsEveryDeclaredCache: the 2026-11-26 cicd machine, where two
// GitHub runner caches outside the shared GOCACHE grew to 430G with nothing
// trimming them. A cache in go_caches, or passed for one pass, is trimmed by
// the same rules as the shared one, to its own budget; the shared cache named
// again is trimmed once; a declared path that is not a Go cache is left alone.
func TestGoCachesTrimsEveryDeclaredCache(t *testing.T) {
	shared, declared, passed, notCache := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	var old, fresh []string
	for _, root := range []string{shared, declared, passed} {
		makeBuildCache(t, root)
		old = append(old, putEntry(t, root, "ab", hexName(1, "-a"), 10, 30*time.Hour))
		fresh = append(fresh, putEntry(t, root, "ab", hexName(2, "-a"), 10, 3*time.Hour))
	}
	stray := putEntry(t, notCache, "ab", hexName(1, "-a"), 10, 1000*time.Hour)

	var out strings.Builder
	r := goRunner(t, &out)
	r.GoCacheRoot, r.GolangciCacheRoot = shared, t.TempDir()
	r.Policy.GoCaches = []string{declared, shared, notCache, "relative/go-build"}
	r.ExtraGoCacheRoots = []string{passed, declared + "/"}
	trims := map[string]GoCacheTrim{}
	r.OnGoCacheTrim = func(tr GoCacheTrim) {
		if _, dup := trims[tr.Root]; dup {
			t.Errorf("%s trimmed twice in one pass", tr.Root)
		}
		trims[tr.Root] = tr
	}
	if err := r.GoCaches(true); err != nil {
		t.Fatal(err)
	}
	for i := range old {
		if exists(old[i]) || !exists(fresh[i]) {
			t.Errorf("cache %d: an entry unused 30h must go under the 24h age rule and one used 3h ago stay (old=%v fresh=%v)\n%s", i, exists(old[i]), exists(fresh[i]), out.String())
		}
	}
	if !exists(stray) {
		t.Error("a declared directory with no Go cache README was trimmed")
	}
	for _, root := range []string{shared, declared, passed} {
		if tr, ok := trims[root]; !ok || tr.BudgetBytes == 0 {
			t.Errorf("%s has no trim record with its own budget: %+v", root, trims)
		}
	}
	if tr := trims[declared]; tr.Cache != "go build cache "+declared {
		t.Errorf("a declared cache is labelled %q, want it named by its path", tr.Cache)
	}
}
