package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/internal/openfiles"
)

// A deadline that hits mid-sweep must leave behind everything it already
// removed. Before the sweep removed as it went, it measured every candidate
// first and deleted none of them if the deadline came during that walk.
func TestTempSweepCutOffStillRemovesWhatItReached(t *testing.T) {
	root := t.TempDir()
	oldest := writeTempEntry(t, root, "go-link-a", 100*time.Hour)
	middle := writeTempEntry(t, root, "go-link-b", 80*time.Hour)
	newest := writeTempEntry(t, root, "go-link-c", 60*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out strings.Builder
	s := newTempSweep(t, root, nil, nil, &out)
	s.ctx = ctx
	s.print = func(format string, args ...any) { out.WriteString(fmt.Sprintf(format, args...)) }
	// The deadline lands right after the first candidate is removed. (Every
	// unlink checks the deadline, so a deadline inside an entry stops there.)
	s.del = unpacedDeleter(func(p string) error {
		err := os.Remove(p)
		if p == oldest {
			cancel()
		}
		return err
	})
	err := s.run(true)

	var cut *CutOffError
	if !errors.As(err, &cut) {
		t.Fatalf("a cut-off sweep must return a CutOffError, got %v", err)
	}
	if exists(oldest) {
		t.Fatal("the oldest candidate was reached and must be removed")
	}
	if !exists(middle) || !exists(newest) {
		t.Fatal("candidates past the cut-off must be retained")
	}
	if !strings.Contains(out.String(), "cut off after") {
		t.Fatalf("the sweep must say how far it got:\n%s", out.String())
	}
}

// A missing snapshot still fails closed: nothing is removed and it is not a cut-off.
func TestTempSweepStillFailsClosedWithoutSnapshot(t *testing.T) {
	root := t.TempDir()
	entry := writeTempEntry(t, root, "go-link-a", 100*time.Hour)
	var out strings.Builder
	s := newTempSweep(t, root, nil, errors.New("lsof not found"), &out)
	s.ctx = context.Background()
	if err := s.run(true); err != nil {
		t.Fatalf("a missing snapshot is a skip, got %v", err)
	}
	if !exists(entry) {
		t.Fatal("removed without an open-files snapshot")
	}
}

// One open-files snapshot per pass, however many layers want it.
func TestPassTakesOneOpenFilesSnapshot(t *testing.T) {
	var takes atomic.Int32
	snapshot := func(context.Context) (openfiles.Snapshot, error) {
		takes.Add(1)
		return openfiles.FromPaths([]string{"/nonexistent/forge-test/unrelated"}), nil
	}

	tempRoot := t.TempDir()
	tempEntry := writeTempEntry(t, tempRoot, "go-link-x", 100*time.Hour)

	scan := resolved(t, t.TempDir())
	shared := filepath.Join(scan, "shared-gocache")
	makeBuildCache(t, shared)
	orphan := filepath.Join(scan, ".gocache-old")
	makeBuildCache(t, orphan)
	putEntry(t, orphan, "ab", hexName(1, "-a"), 10, 100*time.Hour)

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

	p := DefaultPolicy()
	p.Builders = nil
	p.SourceCacheKeep = 1
	p.HostPaths = []string{scan}
	r := Runner{
		Policy: p, TempRoot: tempRoot, SourceCacheRoot: sources,
		GoCacheRoot: shared, GoModCacheRoot: filepath.Join(scan, "mod"),
		GolangciCacheRoot: t.TempDir(), GoimportsRoot: t.TempDir(),
		OpenPaths:  snapshot,
		ProcessEnv: func(context.Context) (string, error) { return "x", nil },
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "context inspect") {
				return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
			}
			return nil, nil // no containers, no images
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.NonDisruptiveGC(ctx, true); err != nil {
		t.Fatalf("pass: %v", err)
	}
	// Each of the three snapshot users did its job on the one snapshot.
	if exists(tempEntry) || exists(orphan) {
		t.Fatalf("layers did not run: temp=%v orphan=%v", exists(tempEntry), exists(orphan))
	}
	if _, err := os.Stat(filepath.Join(sources, "app-bbbbbbbbbbbb")); err == nil {
		t.Fatal("source eviction did not run")
	}
	if got := takes.Load(); got != 1 {
		t.Fatalf("the pass took %d open-files snapshots, want exactly 1", got)
	}
}

func TestGCResultSeparatesCutOffFromFailed(t *testing.T) {
	cut := layerErr("temp sweep", &CutOffError{Err: context.DeadlineExceeded})
	hard := layerErr("logs", fmt.Errorf("permission denied"))

	r := NewGCResult(time.Now(), errors.Join(cut, layerErr("go caches: shared build cache", &CutOffError{Err: errors.New("x")})))
	if !r.OK || len(r.FailedLayers) != 0 || strings.Join(r.CutOffLayers, ",") != "go caches: shared build cache,temp sweep" {
		t.Fatalf("all-cut-off pass must be OK and name the layers: %+v", r)
	}
	if !strings.Contains(r.Summary(), "succeeded (cut off") {
		t.Fatalf("summary: %q", r.Summary())
	}

	r = NewGCResult(time.Now(), errors.Join(cut, hard))
	if r.OK || strings.Join(r.FailedLayers, ",") != "logs" || strings.Join(r.CutOffLayers, ",") != "temp sweep" {
		t.Fatalf("a real error still fails the pass: %+v", r)
	}

	// The pass itself running out of budget between layers is a cut-off too.
	r = NewGCResult(time.Now(), context.DeadlineExceeded)
	if !r.OK || len(r.CutOffLayers) != 1 {
		t.Fatalf("bare deadline: %+v", r)
	}
	// An unattributed non-context error is a failure.
	if r = NewGCResult(time.Now(), errors.New("boom")); r.OK {
		t.Fatalf("unattributed error must fail: %+v", r)
	}
}

func TestCutOffPassIsNotAFullGCProblem(t *testing.T) {
	p := DefaultPolicy()
	p.Registries = []Registry{{Container: "k3d-r"}}
	last := NewGCResult(time.Now().Add(-time.Hour), layerErr("temp sweep", &CutOffError{Err: context.DeadlineExceeded}))
	if got := FullGCProblem(p, last, true, time.Now()); got != "" {
		t.Fatalf("a cut-off pass reported as a problem: %q", got)
	}
}

// A layer's slice expiring must not eat the next layer: each gets its own share.
func TestLayerSlicesAreIndependent(t *testing.T) {
	r := Runner{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r = r.beginPass(ctx)
	first, done := r.slice(0.05)
	<-first.Ctx.Done() // the first layer burns its whole slice
	done()
	second, done2 := r.slice(0.5)
	defer done2()
	if second.Ctx.Err() != nil {
		t.Fatal("a later layer inherited the earlier layer's expired slice")
	}
	if dl, _ := second.Ctx.Deadline(); time.Until(dl) < 500*time.Millisecond {
		t.Fatalf("later layer's slice is too small: %v", time.Until(dl))
	}
}
