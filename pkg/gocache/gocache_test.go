package gocache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBudgetIsCappedByTheVolume(t *testing.T) {
	for _, tc := range []struct {
		name     string
		p        Policy
		capacity uint64
		want     uint64
	}{
		{"unknown volume", Default(), 0, DefaultBudgetGiB * GiB},
		{"large volume", Default(), 4096 * GiB, DefaultBudgetGiB * GiB},
		{"small volume", Default(), 100 * GiB, 15 * GiB},
		{"cap off", Policy{BudgetGiB: 80, VolumePercent: 100}, 100 * GiB, 80 * GiB},
		{"zero means default", Policy{}, 0, DefaultBudgetGiB * GiB},
	} {
		if got := tc.p.Budget(tc.capacity); got != tc.want {
			t.Errorf("%s: Budget = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got, want := (Policy{BudgetGiB: 80, VolumePercent: 100}).Target(0), uint64(64*GiB); got != want {
		t.Errorf("Target = %d, want %d (80%% of an 80 GiB budget)", got, want)
	}
}

// TestAgeCutoffNeverReachesInsideTheFloor: the floor is what keeps a trim
// safe beside running builds, so no age setting may cross it.
func TestAgeCutoffNeverReachesInsideTheFloor(t *testing.T) {
	now := time.Now()
	if got := (Policy{Unused: time.Hour}).AgeCutoff(now); !got.Equal(now.Add(-Floor)) {
		t.Errorf("an age rule inside the floor cut at %s, want the floor", now.Sub(got))
	}
	if got := Default().AgeCutoff(now); !got.Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("the default age rule cut at %s, want 24h", now.Sub(got))
	}
	if !(MinFloor > time.Hour && MinFloor < Floor) {
		t.Fatalf("MinFloor %s must sit between cmd/go's 1h restamp interval and Floor %s", MinFloor, Floor)
	}
}

func TestIsCache(t *testing.T) {
	dir := t.TempDir()
	if IsCache(dir) {
		t.Fatal("a directory with no README is not a cache")
	}
	for readme, want := range map[string]bool{
		"This directory holds cached build artifacts from the Go build system.\n": true,
		"This directory holds cached build artifacts from golangci-lint.\n":       true,
		"hello\n": false,
	} {
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte(readme), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := IsCache(dir); got != want {
			t.Errorf("README %q: IsCache = %v, want %v", readme, got, want)
		}
	}
}
