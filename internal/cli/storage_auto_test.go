package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/storage"
)

// stubAutoGCLauncher replaces the background launcher and reports whether it
// was asked to start a pass.
func stubAutoGCLauncher(t *testing.T) *bool {
	t.Helper()
	launched := false
	orig := startAutoGCFn
	startAutoGCFn = func(string) (string, error) {
		launched = true
		return "/fake/auto-gc.log", nil
	}
	t.Cleanup(func() { startAutoGCFn = orig })
	return &launched
}

// TestOpportunisticGC_DoesNotDelayUp is M3. The pass ran synchronously at the
// end of `forge env up`, so up did not return until it finished — up to its
// 2-minute budget, and longer, since the budget did not bound the host layers.
// It now starts in the background and up returns at once.
func TestOpportunisticGC_DoesNotDelayUp(t *testing.T) {
	storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return true }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
	nonDisruptiveGCFn = func(ctx context.Context, _ storage.Runner) error {
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
		return nil
	}
	launched := stubAutoGCLauncher(t)

	start := time.Now()
	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("env up waited %s for storage maintenance", elapsed.Round(time.Millisecond))
	}
	if !*launched {
		t.Fatalf("no background pass was started:\n%s", out.String())
	}
}

// TestOpportunisticGC_OptOut: FORGE_STORAGE_AUTO=0 turns the pass off.
func TestOpportunisticGC_OptOut(t *testing.T) {
	storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return true }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
	ran := false
	nonDisruptiveGCFn = func(context.Context, storage.Runner) error { ran = true; return nil }
	launched := stubAutoGCLauncher(t)
	t.Setenv("FORGE_STORAGE_AUTO", "0")

	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out)
	if ran || *launched {
		t.Fatalf("FORGE_STORAGE_AUTO=0 still ran the pass (inline=%v background=%v)", ran, *launched)
	}
}

// TestStartAutoGCRefusesUnderTest: the real launcher re-executes the running
// binary, which in a test is the test binary. A test that forgot the stub
// would start a detached copy of the suite — which happened while this was
// being written — so the launcher refuses rather than relying on the stub.
func TestStartAutoGCRefusesUnderTest(t *testing.T) {
	if _, err := startAutoGC(t.TempDir() + "/storage.json"); !errors.Is(err, errAutoGCUnderTest) {
		t.Fatalf("startAutoGC under test: %v, want errAutoGCUnderTest", err)
	}
}

// TestRunAutoGCIsBoundedAndRecorded: the background pass itself runs under the
// budget's deadline and records its attempt, success or not.
func TestRunAutoGCIsBoundedAndRecorded(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	var deadline time.Time
	nonDisruptiveGCFn = func(ctx context.Context, _ storage.Runner) error {
		deadline, _ = ctx.Deadline()
		return errors.New("builder gone")
	}
	if err := runAutoGC(context.Background(), path, io.Discard); err == nil {
		t.Fatal("a failing pass reported success")
	}
	if deadline.IsZero() || time.Until(deadline) > opportunisticGCBudget {
		t.Fatalf("the pass did not run under its budget: deadline %v", deadline)
	}
	if attempt, ok := storage.LastAutoGC(path); !ok || attempt.OK {
		t.Fatalf("attempt not recorded as failed: %+v ok=%v", attempt, ok)
	}
}

// TestUpReclaimsStorage_OnlyForAStackThatRunsHere: the pass shells out to
// docker, so a frontend-only env — nothing of ours on this machine but its dev
// servers — never starts it, and neither does a run that failed its cluster
// gate.
func TestUpReclaimsStorage_OnlyForAStackThatRunsHere(t *testing.T) {
	parse := func(contract string) *KCLEntities {
		t.Helper()
		e, err := parseKCLEntities([]byte(contract))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return e
	}
	frontendOnly := parse(`{"frontends": [{"name": "web", "path": "frontends/web", "port": 3000}]}`)
	hostAPI := parse(`{"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "host", "runner": "go-run"}, "build": {"type": "go", "cmd": "./cmd/api"}, "spec": {"kind": "service"}}]}`)
	for _, tc := range []struct {
		name       string
		e          *KCLEntities
		clusterErr error
		want       bool
	}{
		{"frontend-only env", frontendOnly, nil, false},
		{"host workload", hostAPI, nil, true},
		{"host workload, cluster gate failed", hostAPI, errors.New("rollout failed"), false},
	} {
		if got := upReclaimsStorage(tc.e, tc.clusterErr); got != tc.want {
			t.Errorf("%s: upReclaimsStorage = %v, want %v", tc.name, got, tc.want)
		}
	}
}
