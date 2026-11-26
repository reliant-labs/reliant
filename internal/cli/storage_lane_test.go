package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/storage"
)

// fakeFreeSpace makes every disk a build writes report freeGiB free, so the
// lanes are judged against a known shortfall on any host.
func fakeFreeSpace(t *testing.T, freeGiB uint64) {
	t.Helper()
	old := buildSpaceFn
	buildSpaceFn = func(p storage.Policy, paths ...string) ([]storage.Disk, error) {
		if p.HostReserveGiB > freeGiB {
			return nil, &storage.ReserveError{Path: paths[0], Available: freeGiB * storage.GiB, ReserveGiB: p.HostReserveGiB}
		}
		return nil, nil
	}
	t.Cleanup(func() { buildSpaceFn = old })
}

func policyWithReserve(gib uint64) storage.Policy {
	p := storage.DefaultPolicy()
	p.HostReserveGiB = gib
	return p
}

// Ranger friction #1: a 60 GiB machine reserve refused `env up dev` and
// every air rebuild — a ~30 MB go build — with 39-50 GiB free. A host build
// is refused only below storage.HostBuildReserveGiB, and warned between that
// floor and the policy's reserve; an image (heavy) build keeps the full
// reserve.
func TestAdmitBuildJudgesEachLaneAgainstItsOwnFloor(t *testing.T) {
	cases := []struct {
		name     string
		free     uint64
		reserve  uint64
		lane     buildLane
		override uint64
		refused  bool
		warned   bool
	}{
		{name: "heavy below the reserve is refused", free: 39, reserve: 60, lane: laneHeavy, refused: true},
		{name: "host below the reserve proceeds with a warning", free: 39, reserve: 60, lane: laneHost, warned: true},
		{name: "host below its own floor is refused", free: storage.HostBuildReserveGiB - 2, reserve: 60, lane: laneHost, refused: true},
		{name: "light below the host floor still proceeds", free: storage.HostBuildReserveGiB - 2, reserve: 60, lane: laneLight, warned: true},
		{name: "host above the reserve is silent", free: 50, reserve: 20, lane: laneHost},
		{name: "a policy reserve under the host floor stays the floor", free: 4, reserve: 5, lane: laneHost, refused: true},
		{name: "an override admits a heavy build above it, silently", free: 39, reserve: 60, lane: laneHeavy, override: 30},
		{name: "an override is exact, whatever the lane", free: 39, reserve: 60, lane: laneHost, override: 45, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeFreeSpace(t, tc.free)
			var warn bytes.Buffer
			err := admitBuild(policyWithReserve(tc.reserve), tc.lane, tc.override, &warn, "/project")
			var short *storage.ReserveError
			if tc.refused != errors.As(err, &short) {
				t.Fatalf("admitBuild = %v, refused want %v", err, tc.refused)
			}
			if got := warn.Len() > 0; got != tc.warned {
				t.Fatalf("warned = %v (%q), want %v", got, warn.String(), tc.warned)
			}
			if tc.warned {
				for _, want := range []string{"/project", "GiB free", "below the machine's 60 GiB storage reserve", "building anyway", string(tc.lane), "forge storage gc --apply"} {
					if !strings.Contains(warn.String(), want) {
						t.Errorf("warning %q lacks %q", warn.String(), want)
					}
				}
			}
		})
	}
}

// `forge storage check` is what the scaffolded .air.toml runs before every
// rebuild. Its lane comes from --lane, else $FORGE_STORAGE_LANE (which env up
// sets to host for its host processes), else heavy; its reserve from
// --reserve, else $FORGE_STORAGE_RESERVE_GIB, else the policy.
func TestStorageCheckTakesItsLaneAndReserveFromFlagsOrEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := storage.Save(path, policyWithReserve(60)); err != nil {
		t.Fatal(err)
	}
	fakeFreeSpace(t, 39)
	cases := []struct {
		name    string
		env     map[string]string
		args    []string
		refused bool
	}{
		{name: "bare check is the heavy lane", refused: true},
		{name: "env up's host lane admits a rebuild", env: map[string]string{storageLaneEnv: "host"}},
		{name: "--lane wins over the env", env: map[string]string{storageLaneEnv: "host"}, args: []string{"--lane", "heavy"}, refused: true},
		{name: "--lane host", args: []string{"--lane", "host"}},
		{name: "--reserve for this run", args: []string{"--reserve", "30"}},
		{name: "the reserve env var for this run", env: map[string]string{storage.ReserveOverrideEnv: "30"}},
		{name: "--reserve wins over the env var", env: map[string]string{storage.ReserveOverrideEnv: "30"}, args: []string{"--reserve", "50"}, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(storageLaneEnv, "")
			t.Setenv(storage.ReserveOverrideEnv, "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cmd := newStorageCmd()
			var stderr bytes.Buffer
			cmd.SetErr(&stderr)
			cmd.SetOut(&stderr)
			cmd.SetArgs(append([]string{"--policy", path, "check"}, append(tc.args, t.TempDir())...))
			err := cmd.ExecuteContext(context.Background())
			if (err != nil) != tc.refused {
				t.Fatalf("check = %v, refused want %v", err, tc.refused)
			}
		})
	}
	for name, env := range map[string]map[string]string{
		"unknown lane":      {storageLaneEnv: "medium"},
		"malformed reserve": {storage.ReserveOverrideEnv: "lots"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(storageLaneEnv, "")
			t.Setenv(storage.ReserveOverrideEnv, "")
			for k, v := range env {
				t.Setenv(k, v)
			}
			cmd := newStorageCmd()
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{"--policy", path, "check", t.TempDir()})
			if err := cmd.ExecuteContext(context.Background()); err == nil {
				t.Fatal("a malformed setting was accepted")
			}
		})
	}
}

// Every host process env up launches gets the host lane, so the
// `forge storage check ./tmp` in an EXISTING project's .air.toml admits its
// rebuild under the host floor with no file edit — unless the developer set
// the lane themselves.
func TestHostProcessesAdmitUnderTheHostLane(t *testing.T) {
	got := withForgeShimOnPath([]string{"PATH=/usr/bin"}, "api")
	if lookupEnv(got, storageLaneEnv) != string(laneHost) {
		t.Errorf("host env %v lacks %s=host", got, storageLaneEnv)
	}
	mine := withHostStorageLane([]string{storageLaneEnv + "=heavy"})
	if len(mine) != 1 || lookupEnv(mine, storageLaneEnv) != "heavy" {
		t.Errorf("the developer's own lane was replaced: %v", mine)
	}
}
