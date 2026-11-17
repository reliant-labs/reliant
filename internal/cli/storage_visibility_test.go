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
	maybeOpportunisticGC(t.Context(), &out)
	if got, _ := storage.LastFullGC(path); !got.OK {
		t.Fatalf("a failed opportunistic pass overwrote the full-GC record: %+v", got)
	}
	attempt, ok := storage.LastAutoGC(path)
	if !ok || attempt.OK {
		t.Fatalf("the failed attempt must still be recorded (as failed) to rate-limit retries: %+v ok=%v", attempt, ok)
	}
}

// TestUpSaysWhenRegistryRetentionIsNotRunning: `forge env up` prints one line
// when the full GC has never succeeded, failed last time, or is stale — and
// nothing when it is healthy.
func TestUpSaysWhenRegistryRetentionIsNotRunning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record *storage.GCResult
		want   string
	}{
		{"failed", &storage.GCResult{At: time.Now().Add(-time.Hour), FailedLayers: []string{"registry k3d-cp-registry"}}, "failed in registry k3d-cp-registry"},
		{"stale", &storage.GCResult{At: time.Now().Add(-96 * time.Hour), OK: true}, "4d ago"},
		{"never", nil, "no full storage GC has ever completed"},
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
			maybeOpportunisticGC(t.Context(), &out)
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
