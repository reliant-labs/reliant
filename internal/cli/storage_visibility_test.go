package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/storage"
)

// TestOpportunisticGC_FailureIsNotRecordedAsMaintenance: the opportunistic
// pass stamped "last GC" even when it failed — and it never runs the registry
// layer at all — so doctor reported a working schedule on a machine where
// registry retention had never succeeded.
func TestOpportunisticGC_FailureIsNotRecordedAsMaintenance(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return true }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
	nonDisruptiveGCFn = func(context.Context, storage.Runner) error {
		return errors.New("docker daemon is not reachable")
	}

	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out, upTestEnvRegistries)
	if got, _ := storage.LastFullGC(path); !got.OK {
		t.Fatalf("a failed opportunistic pass overwrote the full-GC record: %+v", got)
	}
	attempt, ok := storage.LastAutoGC(path)
	if !ok || attempt.OK {
		t.Fatalf("the failed attempt must still be recorded (as failed) to rate-limit retries: %+v ok=%v", attempt, ok)
	}
}

// TestUpSaysWhenRegistryRetentionIsNotRunning: `forge env up` prints one line
// when cleanup of a registry this env uses has never succeeded, failed last
// time, or is stale — and nothing when it is healthy.
func TestUpSaysWhenRegistryRetentionIsNotRunning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record *storage.GCResult
		want   string
	}{
		{"failed", &storage.GCResult{At: time.Now().Add(-time.Hour), FailedLayers: []string{"registry k3d-cp-registry"}}, "the last cleanup of registry k3d-cp-registry (1h ago) failed"},
		{"stale", &storage.GCResult{At: time.Now().Add(-96 * time.Hour), OK: true}, "4d ago"},
		{"never", nil, "no cleanup of registry k3d-cp-registry has completed yet"},
		{"healthy", &storage.GCResult{At: time.Now().Add(-time.Hour), OK: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := storagePolicyForOpportunisticGC(t)
			origSchedule := storageScheduleInstalledFn
			storageScheduleInstalledFn = func() bool { return true }
			t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
			if err := storage.RecordAutoGC(path, storage.GCResult{At: time.Now(), OK: true}); err != nil {
				t.Fatal(err)
			}
			if tc.record != nil {
				if err := storage.RecordFullGC(path, *tc.record); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(storage.FullGCRecordPath(path)); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			maybeOpportunisticGC(t.Context(), &out, upTestEnvRegistries)
			lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1
			if tc.want == "" {
				if out.Len() != 0 {
					t.Fatalf("printed with retention healthy:\n%s", out.String())
				}
				return
			}
			if !strings.Contains(out.String(), tc.want) || lines != 1 {
				t.Fatalf("want ONE line containing %q, got:\n%s", tc.want, out.String())
			}
		})
	}
}

// TestStorageGCApplyRecordsItsResult: the full pass is the one that records
// whether retention ran — success or which layers failed.
func TestStorageGCApplyRecordsItsResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := storage.Save(path, storage.DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	orig := fullGCFn
	t.Cleanup(func() { fullGCFn = orig })
	fullGCFn = func(context.Context, storage.Runner, bool) error {
		return errors.New("registry k3d-reg: protected set unavailable")
	}
	cmd := newStorageCmd()
	cmd.SetArgs([]string{"--policy", path, "gc", "--apply"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("a failing pass must still fail the command")
	}
	got, ok := storage.LastFullGC(path)
	if !ok || got.OK || !strings.Contains(got.Error, "protected set unavailable") {
		t.Fatalf("the failed full pass was not recorded: %+v ok=%v", got, ok)
	}

	fullGCFn = func(context.Context, storage.Runner, bool) error { return nil }
	cmd = newStorageCmd()
	cmd.SetArgs([]string{"--policy", path, "gc", "--apply"})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok = storage.LastFullGC(path); !ok || !got.OK {
		t.Fatalf("the successful full pass was not recorded: %+v", got)
	}
}

// TestUpIsSilentAboutRegistriesItDoesNotUse is the brand-new-project case: the
// machine policy and the full-pass record are shared by every project, and a
// fresh project with no local registry (or a different one) printed "the last
// full storage GC (10h ago) failed in registry k3d-control-plane-registry"
// on its first `env up`, naming a registry it never touches.
func TestUpIsSilentAboutRegistriesItDoesNotUse(t *testing.T) {
	for name, env := range map[string][]string{
		"no local registry":      nil,
		"another local registry": {"k3d-simplebutton-registry"},
	} {
		t.Run(name, func(t *testing.T) {
			path := storagePolicyForOpportunisticGC(t)
			if err := storage.RecordAutoGC(path, storage.GCResult{At: time.Now(), OK: true}); err != nil {
				t.Fatal(err)
			}
			if err := storage.RecordFullGC(path, storage.GCResult{At: time.Now().Add(-10 * time.Hour),
				FailedLayers: []string{"registry k3d-cp-registry"}, Error: "registry k3d-cp-registry: boom"}); err != nil {
				t.Fatal(err)
			}
			for _, scheduled := range []bool{true, false} {
				origSchedule := storageScheduleInstalledFn
				storageScheduleInstalledFn = func() bool { return scheduled }
				var out bytes.Buffer
				maybeOpportunisticGC(t.Context(), &out, env)
				storageScheduleInstalledFn = origSchedule
				if out.Len() != 0 {
					t.Fatalf("scheduled=%v: warned about a registry this env does not use:\n%s", scheduled, out.String())
				}
			}
		})
	}
}

// TestUpRegistryFailureLineIsActionable: the line quotes why the recorded pass
// failed, says the env itself is fine, and names the commands to re-check and
// retry, all on one line.
func TestUpRegistryFailureLineIsActionable(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return true }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
	if err := storage.RecordAutoGC(path, storage.GCResult{At: time.Now(), OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.RecordFullGC(path, storage.GCResult{At: time.Now().Add(-10 * time.Hour),
		FailedLayers: []string{"registry k3d-cp-registry"},
		Error:        "registry k3d-cp-registry: registry k3d-cp-registry also serves k3d-e2e; register this context before cleanup"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out, upTestEnvRegistries)
	got := strings.TrimSpace(out.String())
	for _, want := range []string{
		"the last cleanup of registry k3d-cp-registry (10h ago) failed: registry k3d-cp-registry also serves k3d-e2e",
		"This env is unaffected",
		"`forge storage gc --dry-run`",
		"`forge storage gc --apply`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n") {
		t.Errorf("want one line, got:\n%s", got)
	}
}

// declaredRegistryContainers is what scopes that line: every standalone
// registry the env's k3d configs `use`, once each.
func TestDeclaredRegistryContainers(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	use := func(reg string) string {
		return "apiVersion: k3d.io/v1alpha5\nkind: Simple\nregistries:\n  use:\n    - " + reg + ":5000\n"
	}
	a := write("a.yaml", use("k3d-shared-registry"))
	b := write("b.yaml", use("k3d-shared-registry"))
	c := write("c.yaml", use("k3d-other-registry"))
	created := write("d.yaml", "apiVersion: k3d.io/v1alpha5\nkind: Simple\nregistries:\n  create:\n    name: k3d-fresh-registry\n")
	got := declaredRegistryContainers([]ClusterEntity{{Config: a}, {Config: b}, {}, {Config: created}, {Config: c}, {Config: filepath.Join(dir, "missing.yaml")}})
	if strings.Join(got, ",") != "k3d-shared-registry,k3d-other-registry" {
		t.Fatalf("declaredRegistryContainers = %v", got)
	}
	if declaredRegistryContainer([]ClusterEntity{{Config: created}}) != "" {
		t.Fatal("a cluster-owned (registries.create) registry is never registered, so it is not reported")
	}
}
