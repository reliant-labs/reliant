// Copyright (c) 2025 Reliant Labs
package diskguard

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

const gib = diskwatch.GiB

const (
	none      = reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED
	reclaim   = reliantv1.DiskGuardState_DISK_GUARD_STATE_RECLAIMING
	refuseAll = reliantv1.DiskGuardState_DISK_GUARD_STATE_REFUSING_HEAVY
)

// at is a volume of total bytes with frac of it used (df's sense).
func at(total uint64, frac float64) diskwatch.Usage {
	used := uint64(float64(total) * frac)
	return diskwatch.Usage{TotalBytes: total, UsedBytes: used, FreeBytes: total - used}
}

func TestNext_Thresholds(t *testing.T) {
	small := uint64(100 * gib) // 5% of it is 5 GiB: under the free ceiling
	big := uint64(1000 * gib)  // 5% of it is 50 GiB: room for any build
	for _, tc := range []struct {
		name string
		cur  reliantv1.DiskGuardState
		u    diskwatch.Usage
		want reliantv1.DiskGuardState
	}{
		{"below reclaim", none, at(small, 0.89), none},
		{"at reclaim", none, at(small, 0.90), reclaim},
		{"between", none, at(small, 0.94), reclaim},
		{"at refuse", none, at(small, 0.95), refuseAll},
		{"full", reclaim, at(small, 0.99), refuseAll},
		{"a big disk past the percentage with room left is not refused", none, at(big, 0.96), reclaim},
		{"a big disk with under the ceiling free is", none, at(big, 0.985), refuseAll},
		// Hysteresis: each state holds until 2 points below its threshold.
		{"refusal holds just below", refuseAll, at(small, 0.935), refuseAll},
		{"refusal clears past the margin", refuseAll, at(small, 0.925), reclaim},
		{"reclaim holds just below", reclaim, at(small, 0.885), reclaim},
		{"reclaim clears past the margin", reclaim, at(small, 0.875), none},
		{"recovery from refusal all the way", refuseAll, at(small, 0.5), none},
		{"no size", none, diskwatch.Usage{}, none},
	} {
		if got := Next(tc.cur, tc.u, Config{}); got != tc.want {
			t.Errorf("%s: Next(%v, %.1f%%) = %v, want %v", tc.name, tc.cur, 100*tc.u.UsedFraction(), got, tc.want)
		}
	}

	custom := Config{ReclaimAt: 0.70, RefuseAt: 0.80}
	if got := Next(none, at(small, 0.75), custom); got != reclaim {
		t.Errorf("custom reclaim threshold: %v", got)
	}
	if got := Next(none, at(small, 0.81), custom); got != refuseAll {
		t.Errorf("custom refuse threshold: %v", got)
	}
	off := Config{RefuseAt: math.Inf(1)}
	if got := Next(none, at(small, 0.999), off); got != reclaim {
		t.Errorf("refusal off still reclaims: %v", got)
	}
}

func TestConfigFromEnv(t *testing.T) {
	cfg, err := ConfigFromEnv([]string{EnvReclaimAt + "=85", EnvRefuseAt + "=97%"})
	if err != nil || cfg.ReclaimAt != 0.85 || cfg.RefuseAt != 0.97 {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	cfg, err = ConfigFromEnv([]string{EnvRefuseAt + "=off"})
	if err != nil || !math.IsInf(cfg.RefuseAt, 1) || cfg.ReclaimAt != 0 {
		t.Fatalf("off: got %+v, %v", cfg, err)
	}
	cfg, err = ConfigFromEnv([]string{EnvReclaimAt + "=ninety", EnvRefuseAt + "=96"})
	if err == nil || !strings.Contains(err.Error(), EnvReclaimAt) || cfg.RefuseAt != 0.96 || cfg.ReclaimAt != 0 {
		t.Fatalf("a bad value names itself and keeps its default, the rest still apply: %+v, %v", cfg, err)
	}
}

// fakeSource is a diskwatch report the test fills.
type fakeSource struct {
	mu   sync.Mutex
	vols []*reliantv1.DiskVolume
}

func (f *fakeSource) set(vols ...*reliantv1.DiskVolume) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vols = vols
}

func (f *fakeSource) Report() (*reliantv1.DiskReport, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rep := &reliantv1.DiskReport{}
	for _, v := range f.vols {
		rep.Volumes = append(rep.Volumes, proto.Clone(v).(*reliantv1.DiskVolume))
	}
	return rep, len(f.vols) > 0
}

func cacheVolume(frac float64) *reliantv1.DiskVolume {
	u := at(413*gib, frac)
	return &reliantv1.DiskVolume{
		Label: "build caches & docker", Paths: []string{"/var/cache/workspace/go-build", "/var/lib/docker"},
		TotalBytes: u.TotalBytes, FreeBytes: u.FreeBytes, UsedBytes: u.UsedBytes,
		Consumers: []*reliantv1.DiskConsumer{
			{Kind: diskwatch.KindWorktrees, Label: "Workspaces", Bytes: 263 * gib},
			{Kind: diskwatch.KindDockerImages, Label: "Docker images", Bytes: 87 * gib},
		},
	}
}

// fakeReclaimer counts its passes and frees a fixed amount.
type fakeReclaimer struct {
	mu    sync.Mutex
	calls int
	done  chan struct{}
}

func newFakeReclaimer() *fakeReclaimer { return &fakeReclaimer{done: make(chan struct{}, 10)} }

func (r *fakeReclaimer) Reclaim(context.Context) (uint64, string, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	defer func() { r.done <- struct{}{} }()
	return 3 * gib, "Go build cache 3 GiB", nil
}

func (r *fakeReclaimer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// waitPass waits for a reclaim pass to finish recording its outcome.
func waitPass(t *testing.T, g *Guard, r *fakeReclaimer) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no reclaim pass ran")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		busy := g.reclaiming
		g.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the reclaim pass never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGuard_RefusesPastTheThresholdAndRecoversWhenSpaceReturns(t *testing.T) {
	src := &fakeSource{}
	r := newFakeReclaimer()
	now := time.Date(2026, 10, 19, 14, 2, 0, 0, time.UTC)
	g := New(Config{Now: func() time.Time { return now }}, src, r)
	ctx := context.Background()

	src.set(cacheVolume(0.80))
	g.Check(ctx)
	if why := g.HeavyRefusal(); why != "" {
		t.Fatalf("refused at 80%%: %s", why)
	}

	src.set(cacheVolume(0.97))
	g.Check(ctx)
	waitPass(t, g, r)
	why := g.HeavyRefusal()
	for _, want := range []string{
		`"build caches & docker" volume is 97% full`, // which volume, how full
		"past the 95%",
		"Light commands, and work already running, are not affected",
		"Using it: Workspaces ", // what is using it
		", Docker images ",
		"Reclaimed automatically at 14:02", // what was reclaimed
		"Go build cache 3 GiB",
		"To free more: archive the workspaces", // how to free more
		"start again once the volume is below 93%",
	} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal does not say %q:\n%s", want, why)
		}
	}

	// The heartbeat carries the state and what was reclaimed.
	rep, _ := src.Report()
	g.Annotate(rep)
	v := rep.Volumes[0]
	if v.Guard != refuseAll || v.ReclaimedBytes != 3*gib || v.ReclaimedAtMs != now.UnixMilli() || len(v.Reclaimed) != 1 {
		t.Fatalf("annotated volume: %+v", v)
	}

	// Space returns: the next check lifts the refusal.
	src.set(cacheVolume(0.85))
	g.Check(ctx)
	if why := g.HeavyRefusal(); why != "" {
		t.Fatalf("still refusing at 85%%: %s", why)
	}
	rep, _ = src.Report()
	g.Annotate(rep)
	if rep.Volumes[0].Guard != none {
		t.Fatalf("guard state after recovery: %v", rep.Volumes[0].Guard)
	}
}

func TestGuard_ReclaimsOnEntryThenAtMostEveryInterval(t *testing.T) {
	src := &fakeSource{}
	r := newFakeReclaimer()
	now := time.Now()
	g := New(Config{Now: func() time.Time { return now }, ReclaimEvery: time.Hour}, src, r)
	ctx := context.Background()

	src.set(cacheVolume(0.50))
	g.Check(ctx)
	if r.count() != 0 {
		t.Fatal("reclaimed below the threshold")
	}

	src.set(cacheVolume(0.91))
	g.Check(ctx)
	waitPass(t, g, r)
	g.Check(ctx)
	now = now.Add(30 * time.Minute)
	g.Check(ctx)
	if r.count() != 1 {
		t.Fatalf("reclaim passes = %d, want 1 within the interval", r.count())
	}
	now = now.Add(31 * time.Minute)
	g.Check(ctx)
	waitPass(t, g, r)
	if r.count() != 2 {
		t.Fatalf("reclaim passes = %d, want a second once the interval passed", r.count())
	}
	if why := g.HeavyRefusal(); why != "" {
		t.Fatalf("refused while only reclaiming: %s", why)
	}
}

func TestGuard_StateChangeSignalsTheHeartbeat(t *testing.T) {
	src := &fakeSource{}
	g := New(Config{}, src)
	src.set(cacheVolume(0.96))
	g.Check(context.Background())
	select {
	case <-g.Changed():
	case <-time.After(time.Second):
		t.Fatal("entering refusal did not signal")
	}
}

func TestGuard_IgnoresTheSystemVolume(t *testing.T) {
	src := &fakeSource{}
	g := New(Config{}, src)
	sys := at(10*gib, 0.99)
	src.set(&reliantv1.DiskVolume{Label: "system", Paths: []string{"/etc"}, TotalBytes: sys.TotalBytes, FreeBytes: sys.FreeBytes, UsedBytes: sys.UsedBytes})
	g.Check(context.Background())
	if why := g.HeavyRefusal(); why != "" {
		t.Fatalf("a full system-packages volume refused builds: %s", why)
	}
}

func TestNilGuardIsInert(t *testing.T) {
	var g *Guard
	g.Run(context.Background())
	g.Annotate(&reliantv1.DiskReport{})
	if g.HeavyRefusal() != "" || g.Changed() != nil {
		t.Fatal("a nil guard must do nothing")
	}
}

func TestGuard_WithNothingToReclaimTheRefusalClaimsNoCleanUp(t *testing.T) {
	src := &fakeSource{}
	g := New(Config{}, src)
	src.set(cacheVolume(0.97))
	g.Check(context.Background())
	why := g.HeavyRefusal()
	if why == "" || strings.Contains(why, "Reclaim") || !strings.Contains(why, "To free more") {
		t.Fatalf("refusal with no reclaimers:\n%s", why)
	}

	r := newFakeReclaimer()
	g.Register(r)
	g.Check(context.Background())
	waitPass(t, g, r)
	if why := g.HeavyRefusal(); !strings.Contains(why, "Reclaimed automatically") {
		t.Fatalf("a registered reclaimer runs and is reported:\n%s", why)
	}
}
