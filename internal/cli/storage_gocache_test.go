package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/storage"
)

// stubGoCacheGC replaces the Go-cache pass for one test.
func stubGoCacheGC(t *testing.T, fn func(context.Context, storage.Runner, bool) error) {
	t.Helper()
	orig := goCacheGCFn
	goCacheGCFn = fn
	t.Cleanup(func() { goCacheGCFn = orig })
}

// TestGoCacheGCRecordsWhatEachTrimDid: an applied pass records its outcome
// and every cache's report in its own record, under its budget, and leaves the
// opportunistic pass's record alone — a scheduler runs the two on different
// cadences and rate-limits each by its own last run.
func TestGoCacheGCRecordsWhatEachTrimDid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	var deadline time.Time
	stubGoCacheGC(t, func(ctx context.Context, r storage.Runner, apply bool) error {
		deadline, _ = ctx.Deadline()
		if !apply {
			t.Error("--apply was not passed through")
		}
		r.OnGoCacheTrim(storage.GoCacheTrim{Cache: "shared go build cache", Removed: 7, Finished: false, ShardsCovered: 3, ShardsTotal: 256})
		return fmt.Errorf("go caches: %w", &storage.LayerError{Layer: "go caches: shared build cache",
			Err: &storage.CutOffError{Err: context.DeadlineExceeded}})
	})
	var out bytes.Buffer
	if err := runGoCacheGC(context.Background(), storage.DefaultPolicy(), path, &out, true, time.Minute); err != nil {
		t.Fatalf("a cut-off pass is not a failure: %v", err)
	}
	if deadline.IsZero() || time.Until(deadline) > time.Minute {
		t.Fatalf("the pass did not run under its budget: deadline %v", deadline)
	}
	rec, ok := storage.LastGoCacheGC(path)
	if !ok || !rec.OK || strings.Join(rec.CutOffLayers, ",") != "go caches: shared build cache" {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
	if len(rec.GoCaches) != 1 || rec.GoCaches[0].Removed != 7 || rec.GoCaches[0].ShardsCovered != 3 {
		t.Fatalf("the trim's report is not in the record: %+v", rec.GoCaches)
	}
	if _, ok := storage.LastAutoGC(path); ok {
		t.Fatal("the Go-cache pass wrote the opportunistic pass's record")
	}
	if !strings.Contains(out.String(), "storage gocache: succeeded (cut off") {
		t.Fatalf("output does not say the pass was cut off:\n%s", out.String())
	}
}

// TestGoCacheGCDryRunRecordsNothing: a preview changes nothing, so it is no
// evidence the cache is being trimmed and must not reset a scheduler's clock.
func TestGoCacheGCDryRunRecordsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	stubGoCacheGC(t, func(context.Context, storage.Runner, bool) error { return nil })
	if err := runGoCacheGC(context.Background(), storage.DefaultPolicy(), path, io.Discard, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := storage.LastGoCacheGC(path); ok {
		t.Fatal("a dry run was recorded")
	}
}

// TestGoCacheGCFailureIsRecordedAndReturned: a cache that cannot be located is
// a failed pass, in the record and the exit status.
func TestGoCacheGCFailureIsRecordedAndReturned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	stubGoCacheGC(t, func(context.Context, storage.Runner, bool) error {
		return &storage.LayerError{Layer: "go caches: shared build cache", Err: errors.New("locate the shared Go build cache: go env: exit status 1")}
	})
	if err := runGoCacheGC(context.Background(), storage.DefaultPolicy(), path, io.Discard, true, 0); err == nil {
		t.Fatal("a failed pass reported success")
	}
	if rec, ok := storage.LastGoCacheGC(path); !ok || rec.OK || strings.Join(rec.FailedLayers, ",") != "go caches: shared build cache" {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
}

// TestAutoGCRecordsGoCacheTrims: the opportunistic pass's record carries the
// same per-cache reports, so its scheduler sees them too.
func TestAutoGCRecordsGoCacheTrims(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	nonDisruptiveGCFn = func(_ context.Context, r storage.Runner) error {
		r.OnGoCacheTrim(storage.GoCacheTrim{Cache: "shared go build cache", Finished: true})
		return nil
	}
	if err := runAutoGC(context.Background(), path, io.Discard); err != nil {
		t.Fatal(err)
	}
	if rec, ok := storage.LastAutoGC(path); !ok || len(rec.GoCaches) != 1 || !rec.GoCaches[0].Finished {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
}
