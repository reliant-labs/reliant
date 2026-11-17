package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/storage"
)

// impossibleReservePolicy points FORGE_STORAGE_POLICY at a policy whose host
// reserve no disk can meet, so the REAL admission check — statfs and all —
// is guaranteed to come up short on whatever machine runs this.
func impossibleReservePolicy(t *testing.T) {
	t.Helper()
	p := storage.DefaultPolicy()
	p.HostReserveGiB = 1 << 30
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := storage.Save(path, p); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_STORAGE_POLICY", path)
}

// pinCI pins buildinfo.IsCI for one test without touching the process's CI
// variables, which a runner sets and a developer's shell does not.
func pinCI(t *testing.T, ci bool) {
	t.Helper()
	buildinfo.SetCI(ci)
	t.Cleanup(buildinfo.ClearCI)
}

// TestBuildStorageAdmitsAShortRunnerOnCI is the control-plane CI failure:
// `forge env build` on a stock GitHub runner (~14 GiB free at job start, 1–2
// GiB by the time the suite reaches a build) was refused outright —
//
//	host disk /tmp/... has 2.3 GiB free, below the 20 GiB reserve; run
//	'forge storage status' and 'forge storage gc --apply' before building
//
// — although a runner's disk is discarded with the job and the remedy names
// caches a fresh runner does not have. The reserve exists to stop a
// PERSISTENT machine filling up; on an ephemeral runner it is a report.
func TestBuildStorageAdmitsAShortRunnerOnCI(t *testing.T) {
	impossibleReservePolicy(t)
	t.Setenv(enforceReserveEnv, "")
	pinCI(t, true)

	var stderr bytes.Buffer
	if err := checkBuildStorage(t.TempDir(), &stderr); err != nil {
		t.Fatalf("a CI runner below the reserve was refused: %v", err)
	}
	// Admitted, but never silently: the shortfall is still the first thing
	// to read when the build then dies of ENOSPC.
	got := stderr.String()
	for _, want := range []string{"below the", "reserve", "CI runner", enforceReserveEnv} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning does not say %q:\n%s", want, got)
		}
	}
}

// TestBuildStorageStillRefusesOffCI is the control: the developer machine
// the reserve was written for keeps the refusal, byte for byte.
func TestBuildStorageStillRefusesOffCI(t *testing.T) {
	impossibleReservePolicy(t)
	t.Setenv(enforceReserveEnv, "")
	pinCI(t, false)

	var stderr bytes.Buffer
	err := checkBuildStorage(t.TempDir(), &stderr)
	var reserve *storage.ReserveError
	if !errors.As(err, &reserve) {
		t.Fatalf("a developer machine below the reserve was admitted: err=%v", err)
	}
	if !strings.Contains(err.Error(), "forge storage gc --apply") {
		t.Errorf("the refusal no longer names the command that unblocks it: %v", err)
	}
}

// TestBuildStorageEnforcesOnCIWhenAsked: a PERSISTENT self-hosted runner is
// a machine that fills up, so the default is a default and not a wall.
func TestBuildStorageEnforcesOnCIWhenAsked(t *testing.T) {
	impossibleReservePolicy(t)
	t.Setenv(enforceReserveEnv, "1")
	pinCI(t, true)

	var reserve *storage.ReserveError
	if err := checkBuildStorage(t.TempDir(), &bytes.Buffer{}); !errors.As(err, &reserve) {
		t.Fatalf("%s=1 on CI did not refuse: err=%v", enforceReserveEnv, err)
	}
}

// TestBuildStorageRefusesUnreadableDiskOnCI: only a measured SHORTFALL is
// relaxed. A disk forge could not read is an unknown, and an unknown is not
// waved through on the strength of being on a runner.
func TestBuildStorageRefusesUnreadableDiskOnCI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(path, []byte(`{"registry_keep":`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_STORAGE_POLICY", path)
	pinCI(t, true)

	if err := checkBuildStorage(t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("an unloadable storage policy was admitted on CI")
	}
}
