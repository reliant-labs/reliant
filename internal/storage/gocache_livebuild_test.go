package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParsePsElapsed(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:05":       5 * time.Second,
		"12:34":       12*time.Minute + 34*time.Second,
		"1:02:03":     time.Hour + 2*time.Minute + 3*time.Second,
		"2-03:04:05":  48*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second,
		"10-00:00:00": 240 * time.Hour,
	} {
		got, ok := parsePsElapsed(in)
		if !ok || got != want {
			t.Errorf("parsePsElapsed(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "5", "a:b", "1-2", "1:2:3:4", "-1:00"} {
		if d, ok := parsePsElapsed(in); ok {
			t.Errorf("parsePsElapsed(%q) = %v, want a parse failure", in, d)
		}
	}
}

func TestParsePsToolchainAgesKeepsOnlyTheToolchain(t *testing.T) {
	out := strings.Join([]string{
		"   10:00 go",
		" 1:05:00 /usr/local/go/pkg/tool/linux_amd64/compile",
		"   02:00 golangci-lint",
		"   99:00 bash",
		"   99:00 gopls",
		" 3-00:00:00 vet",
		"garbage",
	}, "\n")
	got := parsePsToolchainAges(out)
	want := []time.Duration{10 * time.Minute, time.Hour + 5*time.Minute, 2 * time.Minute, 72 * time.Hour}
	if len(got) != len(want) {
		t.Fatalf("ages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ages = %v, want %v", got, want)
		}
	}
}

// overFloorCache is a cache far over budget whose entries are 3h and 5h old:
// everything is past the 2h floor, so only a live build can protect it.
func overFloorCache(t *testing.T, ages func(context.Context) ([]time.Duration, error)) (Runner, string, *strings.Builder) {
	t.Helper()
	root := t.TempDir()
	fillShards(t, root, 16, 40, func(_, i int) time.Duration {
		if i < 20 {
			return 3 * time.Hour
		}
		return 5 * time.Hour
	})
	prev := goCacheDiskSpace
	goCacheDiskSpace = func(path string) (Disk, error) { return Disk{Path: path, Capacity: 100_000}, nil }
	t.Cleanup(func() { goCacheDiskSpace = prev })
	out := &strings.Builder{}
	r := goRunner(t, out)
	r.GoCacheRoot = root
	r.Policy.GoCacheVolumePercent = 1 // 1 000 B against 64 000 B
	r.Policy.DeleteRatePerSec = 1_000_000_000
	r.Policy.MaxDeletesPerPass = 1_000_000
	r.LiveToolchainAges = ages
	return r, root, out
}

func ageOf(d ...time.Duration) func(context.Context) ([]time.Duration, error) {
	return func(context.Context) ([]time.Duration, error) { return d, nil }
}

// A build that has run for 2.5h can hold an entry as old as 2.5h+1h: the floor
// alone would have deleted the 3h-old entries under it. The lease keeps them,
// and removes the 5h-old ones nothing could be holding.
func TestGoCacheTrimNeverDeletesWhatALongBuildCanHold(t *testing.T) {
	r, root, out := overFloorCache(t, ageOf(150*time.Minute, 2*time.Minute))
	err := r.SharedGoCache(true)
	var ub *BudgetUnreachableError
	if !errors.As(err, &ub) {
		t.Fatalf("a budget the live build protects must be reported as unreachable, got %v\n%s", err, out)
	}
	if got := countEntries(root); got != 16*20 {
		t.Fatalf("%d entries left, want the %d that are 3h old (a 2.5h build can hold them) and none of the 5h old\n%s", got, 16*20, out)
	}
	if !strings.Contains(out.String(), "held back by a running Go build 2h30m0s old") {
		t.Fatalf("the pass does not say a running build held it back:\n%s", out)
	}
}

// The control: with no build running, the same cache is trimmed past the
// floor, 3h-old entries included. Proves the lease, not the floor, kept them.
func TestGoCacheTrimWithoutALiveBuildReachesThePastFloor(t *testing.T) {
	r, root, out := overFloorCache(t, ageOf())
	_ = r.SharedGoCache(true)
	if got := countEntries(root); got != 0 {
		t.Fatalf("%d entries left with no build running, want 0\n%s", got, out)
	}
}

// A build younger than about an hour is already covered by the floor and
// changes nothing; so does a server that has run for days.
func TestGoCacheLiveBuildBoundIgnoresYoungBuildsAndServers(t *testing.T) {
	for name, ages := range map[string][]time.Duration{
		"young build":            {20 * time.Minute, 55 * time.Minute},
		"week-old go run server": {7 * 24 * time.Hour},
		"go run server past 3h":  {4 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			r, root, out := overFloorCache(t, ageOf(ages...))
			_ = r.SharedGoCache(true)
			if got := countEntries(root); got != 0 {
				t.Fatalf("%d entries left, want 0: %s must not hold the cache\n%s", got, name, out)
			}
			if strings.Contains(out.String(), "held back") {
				t.Fatalf("%s held the cutoff back:\n%s", name, out)
			}
		})
	}
}

// Fail closed: a process table that cannot be read is treated as a build as
// old as any that counts, not as no builds.
func TestGoCacheLiveBuildBoundFailsClosed(t *testing.T) {
	r, root, out := overFloorCache(t, func(context.Context) ([]time.Duration, error) { return nil, errors.New("ps: not permitted") })
	_ = r.SharedGoCache(true)
	// Assumed a 3h build: cutoff 4h05m, so the 3h-old entries stay and the
	// 5h-old ones go. Without the assumption all of them would go.
	if got := countEntries(root); got != 16*20 {
		t.Fatalf("%d entries left, want the %d that are 3h old: an unreadable process table must not license a trim past the longest build\n%s", got, 16*20, out)
	}
	if !strings.Contains(out.String(), "cannot list running builds") {
		t.Fatalf("the failure is silent:\n%s", out)
	}
}

func TestGoCacheLiveBuildBoundValue(t *testing.T) {
	now := time.Now()
	r := Runner{LiveToolchainAges: ageOf(90*time.Minute, 10*time.Minute)}
	bound, oldest, ok := r.liveBuildBound(context.Background(), now)
	if !ok || oldest != 90*time.Minute || !bound.Equal(now.Add(-90*time.Minute-liveBuildMargin)) {
		t.Fatalf("bound = %v, oldest = %v, ok = %v", now.Sub(bound), oldest, ok)
	}
	if _, _, ok := (Runner{LiveToolchainAges: ageOf()}).liveBuildBound(context.Background(), now); ok {
		t.Fatal("no builds must not constrain the cutoff")
	}
}
