package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/internal/openfiles"
)

// unpacedDeleter is a governor fast enough for tests that are not about
// pacing. remove nil means os.Remove.
func unpacedDeleter(remove func(string) error) deleter {
	return newDeleter(Policy{DeleteRatePerSec: 1_000_000_000}, remove, nil)
}

// emfile is the error virtiofs returned on 2026-10-09 once its host daemon was
// out of descriptors.
func emfile(path string) error {
	return &fs.PathError{Op: "unlinkat", Path: path, Err: syscall.EMFILE}
}

func TestFsDistressIsTheFilesystemFailingNotOneEntry(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE, syscall.ENOTCONN, syscall.EIO} {
		if !fsDistress(&fs.PathError{Op: "unlinkat", Path: "/x", Err: errno}) {
			t.Errorf("%v must back the pass off", errno)
		}
	}
	for _, err := range []error{fs.ErrNotExist, fs.ErrPermission, &fs.PathError{Op: "unlinkat", Path: "/x", Err: syscall.ENOTEMPTY}, errors.New("x")} {
		if fsDistress(err) {
			t.Errorf("%v is one entry's problem, not distress", err)
		}
	}
}

// Unlinks come in batches, and a batch may not complete faster than the rate.
// The pause stops every worker because it is taken under the pass's lock.
func TestDeleterPacesInBatches(t *testing.T) {
	var pauses []time.Duration
	d := newDeleter(Policy{DeleteRatePerSec: 1000}, nil, func(_ context.Context, wait time.Duration) error {
		pauses = append(pauses, wait)
		return nil
	})
	for i := 0; i < 5*deleteBatch; i++ {
		if err := d.grant(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(pauses) != 4 {
		t.Fatalf("%d pauses for 5 batches, want 4 (one before each batch after the first)", len(pauses))
	}
	for _, p := range pauses {
		if p <= 0 || p > time.Second*deleteBatch/1000 {
			t.Fatalf("pause %v outside (0, %v]", p, time.Second*deleteBatch/1000)
		}
	}
}

// The cap is a cut-off, not a failure: the rest is retained for the next pass.
// A share leaves the remainder of the budget for the layers after it.
func TestDeleterCapAndShares(t *testing.T) {
	d := newDeleter(Policy{MaxDeletesPerPass: 100, DeleteRatePerSec: 1_000_000_000}, nil, nil)
	first := d.share(0.6)
	for i := 0; i < 60; i++ {
		if err := first.grant(context.Background()); err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}
	var cut *CutOffError
	if err := first.grant(context.Background()); !errors.As(err, &cut) || !errors.Is(err, errDeleteCap) {
		t.Fatalf("a spent share must refuse with the cap's cut-off, got %v", err)
	}
	for i := 0; i < 40; i++ {
		if err := d.grant(context.Background()); err != nil {
			t.Fatalf("the layers after a share lost their budget at %d: %v", i, err)
		}
	}
	if err := d.grant(context.Background()); !errors.As(err, &cut) {
		t.Fatalf("the pass cap must hold, got %v", err)
	}
	if NewGCResult(time.Now(), layerErr("x", d.grant(context.Background()))).OK != true {
		t.Fatal("reaching the cap must not fail the pass")
	}
}

// A tree stopped partway keeps the marker that identifies it, so the next
// pass still recognises it and finishes the job.
func TestRemoveTreeStoppedPartwayKeepsItsMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gocache")
	makeBuildCache(t, root)
	for i := 0; i < 6; i++ {
		putEntry(t, root, fmt.Sprintf("%02x", i), hexName(i, "-a"), 1, 100*time.Hour)
	}
	d := newDeleter(Policy{MaxDeletesPerPass: 4, DeleteRatePerSec: 1_000_000_000}, nil, nil)
	err := d.removeTree(context.Background(), root, "README")
	var cut *CutOffError
	if !errors.As(err, &cut) {
		t.Fatalf("want the cap's cut-off, got %v", err)
	}
	if !exists(filepath.Join(root, "README")) {
		t.Fatal("the marker was removed before the rest of the tree")
	}
	if goCacheKind(root) != "build" {
		t.Fatal("a partly removed cache is no longer recognised")
	}
}

// One EMFILE ends all unlinking: the failing remove is the last one tried.
func TestDeleterBacksOffAtTheFirstDistress(t *testing.T) {
	var calls atomic.Int32
	d := unpacedDeleter(func(p string) error {
		if calls.Add(1) == 3 {
			return emfile(p)
		}
		return nil
	})
	var backoff *BackedOffError
	for i := 0; i < 10; i++ {
		err := d.unlink(context.Background(), fmt.Sprintf("/x/%d", i))
		if i < 2 && err != nil {
			t.Fatalf("unlink %d: %v", i, err)
		}
		if i >= 2 && !errors.As(err, &backoff) {
			t.Fatalf("unlink %d after the EMFILE: %v, want the back-off", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("%d removes attempted, want 3: the pass kept hammering after EMFILE", got)
	}
}

// boundedPassFixture is a machine with a shared Go cache, a temp sweep entry
// and a source cache entry, all due for removal, for a NonDisruptiveGC run
// whose every unlink goes through remove.
type boundedPassFixture struct {
	cache, tempEntry, source string
	dockerCalls              atomic.Int32
}

func (f *boundedPassFixture) runner(t *testing.T, p Policy, remove func(string) error, out *strings.Builder) Runner {
	t.Helper()
	return Runner{
		Policy: p, Out: out, removeFn: remove,
		TempRoot: filepath.Dir(f.tempEntry), SourceCacheRoot: filepath.Dir(f.source),
		GoCacheRoot: f.cache, GoModCacheRoot: filepath.Join(t.TempDir(), "mod"),
		GolangciCacheRoot: t.TempDir(), GoimportsRoot: t.TempDir(),
		OpenPaths: func(context.Context) (openfiles.Snapshot, error) {
			return openfiles.FromPaths([]string{"/nonexistent/forge-storage-test/unrelated"}), nil
		},
		ProcessEnv: func(context.Context) (string, error) { return "x", nil },
		Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "docker" {
				f.dockerCalls.Add(1)
				if strings.Contains(strings.Join(args, " "), "context inspect") {
					return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
				}
			}
			return nil, nil
		},
	}
}

func newBoundedPassFixture(t *testing.T, cacheEntries int) *boundedPassFixture {
	t.Helper()
	f := &boundedPassFixture{cache: filepath.Join(t.TempDir(), "go-build")}
	fillShards(t, f.cache, 8, cacheEntries/8, func(int, int) time.Duration { return 100 * time.Hour })
	f.tempEntry = writeTempEntry(t, t.TempDir(), "go-link-abandoned", 100*time.Hour)
	sources := filepath.Join(t.TempDir(), "sources")
	for name, age := range map[string]time.Duration{"app-aaaaaaaaaaaa": 0, "app-bbbbbbbbbbbb": 90 * 24 * time.Hour} {
		marker := filepath.Join(sources, name, gitsource.MetadataFile)
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(`{"repo":"github.com/acme/app","ref":"v1"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		_ = os.Chtimes(marker, at, at)
	}
	f.source = filepath.Join(sources, "app-bbbbbbbbbbbb")
	return f
}

func boundedPolicy() Policy {
	p := DefaultPolicy()
	p.Builders = nil
	p.SourceCacheKeep = 1
	p.DeleteRatePerSec = 1_000_000_000
	return p
}

// TestPassBacksOffAtTheFirstEMFILEAndRetainsTheRest is the 2026-10-09
// trigger: the Go cache trim kept unlinking through EMFILE, because a failed
// unlink was skipped and the next one tried. Now the first EMFILE stops the
// trim, every later layer, and the Docker layers; the rest is retained; and
// the pass is recorded as backed off — not failed, so nothing retries it at
// once — with a line the operator cannot miss.
func TestPassBacksOffAtTheFirstEMFILEAndRetainsTheRest(t *testing.T) {
	f := newBoundedPassFixture(t, 400)
	var removes atomic.Int32
	remove := func(p string) error {
		removes.Add(1)
		return emfile(p)
	}
	var out strings.Builder
	r := f.runner(t, boundedPolicy(), remove, &out)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := r.NonDisruptiveGC(ctx, true)

	if got := removes.Load(); got != 1 {
		t.Fatalf("%d unlinks attempted, want exactly 1: the pass kept going after EMFILE\n%s", got, out.String())
	}
	if n := countEntries(f.cache); n != 400 {
		t.Fatalf("%d cache entries left, want all 400 retained", n)
	}
	if !exists(f.tempEntry) || !exists(f.source) {
		t.Fatalf("a later layer still removed something: temp=%v source=%v", exists(f.tempEntry), exists(f.source))
	}
	if got := f.dockerCalls.Load(); got != 0 {
		t.Fatalf("the Docker layers still ran (%d docker calls) after the back-off", got)
	}
	res := NewGCResult(time.Now(), err)
	if !res.OK || len(res.BackedOffLayers) == 0 || len(res.FailedLayers) != 0 {
		t.Fatalf("want an OK, backed-off record, got %+v (err %v)", res, err)
	}
	if RealFailure(err) != nil {
		t.Fatalf("a back-off must not fail the command (which invites an immediate retry): %v", err)
	}
	if !strings.Contains(res.Summary(), "backed off") || !strings.Contains(out.String(), "BACKED OFF") {
		t.Fatalf("the back-off is not reported: summary %q\n%s", res.Summary(), out.String())
	}
}

// Distress on a READ — the shard listing, before any unlink — backs off too.
func TestGoCacheReadDistressBacksOff(t *testing.T) {
	root := filepath.Join(t.TempDir(), "go-build")
	fillShards(t, root, 4, 5, func(int, int) time.Duration { return 100 * time.Hour })
	shards, _ := goCacheShardList(root)
	d := unpacedDeleter(nil)
	if err := d.observe(filepath.Join(root, "00"), emfile(filepath.Join(root, "00"))); err == nil {
		t.Fatal("observe must return the back-off")
	}
	pass := streamTrimGoCache(context.Background(), root, shards, 0, time.Now().Add(-goCacheFloor), true, d)
	var backoff *BackedOffError
	if !errors.As(pass.stop, &backoff) || pass.removed != 0 || countEntries(root) != 20 {
		t.Fatalf("a backed-off pass unlinked anyway: %+v", pass)
	}
}

// TestPassDeleteCapHoldsAndConverges: one pass unlinks at most
// max_deletes_per_pass entries across every layer, whatever is due; the
// budget is reached over several passes.
func TestPassDeleteCapHoldsAndConverges(t *testing.T) {
	const limit = 50
	f := newBoundedPassFixture(t, 400)
	var removes atomic.Int32
	remove := func(p string) error {
		removes.Add(1)
		return os.Remove(p)
	}
	p := boundedPolicy()
	p.MaxDeletesPerPass = limit
	passes := 0
	for countEntries(f.cache) > 0 {
		if passes++; passes > 20 {
			t.Fatalf("did not converge in 20 passes: %d cache entries left", countEntries(f.cache))
		}
		before := removes.Load()
		var out strings.Builder
		r := f.runner(t, p, remove, &out)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := r.NonDisruptiveGC(ctx, true)
		cancel()
		if got := removes.Load() - before; got > limit {
			t.Fatalf("pass %d unlinked %d entries, over the cap of %d\n%s", passes, got, limit, out.String())
		}
		if RealFailure(err) != nil {
			t.Fatalf("pass %d: a capped pass must not fail: %v\n%s", passes, err, out.String())
		}
		if passes == 1 && len(NewGCResult(time.Now(), err).CutOffLayers) == 0 {
			t.Fatalf("a capped pass must say so (cut off): %v\n%s", err, out.String())
		}
	}
	if passes < 400/limit {
		t.Fatalf("converged in %d passes; the cap of %d cannot have held for 400 entries", passes, limit)
	}
	if exists(f.tempEntry) || exists(f.source) {
		t.Fatalf("the shared cache starved the later layers: temp=%v source=%v", exists(f.tempEntry), exists(f.source))
	}
}

// A source clone stopped partway has already left the cache: Resolve never
// sees a half-deleted pin, and the next pass finishes the leftover.
func TestSourceEvictionStoppedPartwayIsFinishedNextPass(t *testing.T) {
	f := newBoundedPassFixture(t, 8)
	var out strings.Builder
	calls := 0
	r := f.runner(t, boundedPolicy(), func(p string) error {
		if calls++; calls == 1 {
			return emfile(p)
		}
		return os.Remove(p)
	}, &out)
	if err := r.Sources(true); !errors.As(err, new(*BackedOffError)) {
		t.Fatalf("want a back-off, got %v\n%s", err, out.String())
	}
	if exists(f.source) {
		t.Fatal("a half-evicted clone is still under its cache key")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(f.source), ".forge-evicting-*"))
	if len(leftovers) != 1 {
		t.Fatalf("want one leftover to finish, got %v", leftovers)
	}
	r = f.runner(t, boundedPolicy(), nil, &out)
	if err := r.Sources(true); err != nil {
		t.Fatal(err)
	}
	if exists(leftovers[0]) {
		t.Fatalf("the next pass did not finish the interrupted eviction\n%s", out.String())
	}
}

// The temp sweep stops at its first EMFILE too, keeping every entry.
func TestTempSweepBacksOffOnDistress(t *testing.T) {
	root := t.TempDir()
	a := writeTempEntry(t, root, "go-link-a", 100*time.Hour)
	b := writeTempEntry(t, root, "go-link-b", 90*time.Hour)
	var out strings.Builder
	s := newTempSweep(t, root, nil, nil, &out)
	s.ctx = context.Background()
	calls := 0
	s.del = unpacedDeleter(func(p string) error { calls++; return emfile(p) })
	err := s.run(true)
	if !errors.As(err, new(*BackedOffError)) {
		t.Fatalf("want a back-off, got %v\n%s", err, out.String())
	}
	if calls != 1 || !exists(a) || !exists(b) {
		t.Fatalf("the sweep kept going after EMFILE: %d unlinks, a=%v b=%v", calls, exists(a), exists(b))
	}
}

// TestDeleterOverBudgetAllowance: an allowance lets one layer unlink past its
// share without spending the pass's cap, so the layers after it keep theirs.
func TestDeleterOverBudgetAllowance(t *testing.T) {
	d := newDeleter(Policy{MaxDeletesPerPass: 10, DeleteRatePerSec: 1_000_000_000}, nil, nil)
	first := d.share(0.5).allowOver(7)
	granted := 0
	for first.grant(context.Background()) == nil {
		granted++
		if granted > 100 {
			t.Fatal("allowance never ran out")
		}
	}
	if granted != 5+7 {
		t.Fatalf("granted %d, want the 5-entry share plus the 7-entry allowance", granted)
	}
	if got := d.overBudgetUnlinks(); got != 7 {
		t.Fatalf("overBudgetUnlinks = %d, want 7", got)
	}
	later := d.share(1)
	n := 0
	for later.grant(context.Background()) == nil {
		n++
	}
	if n != 5 {
		t.Fatalf("the next layer got %d unlinks, want the 5 the cap still has", n)
	}
}

// TestDeleterOverBudgetAllowanceStillBacksOff: the allowance lifts the cap,
// not the back-off.
func TestDeleterOverBudgetAllowanceStillBacksOff(t *testing.T) {
	d := newDeleter(Policy{MaxDeletesPerPass: 1, DeleteRatePerSec: 1_000_000_000}, emfile, nil).allowOver(100)
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%d", i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		err := d.unlink(context.Background(), p)
		var b *BackedOffError
		if i == 0 && !errors.As(err, &b) {
			t.Fatalf("first EMFILE: %v, want a back-off", err)
		}
	}
	if d.overBudgetUnlinks() != 0 {
		t.Fatal("granted allowance unlinks after the back-off")
	}
}
