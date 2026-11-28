// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/version"
)

func (h *reaperHarness) audit() []trimAuditRecord {
	h.t.Helper()
	f, err := os.Open(filepath.Join(filepath.Dir(h.policy), trimAuditName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []trimAuditRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec trimAuditRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			h.t.Fatalf("audit line is not JSON: %v: %q", err, sc.Text())
		}
		out = append(out, rec)
	}
	return out
}

// trimRecord writes forge's pass record for a Go-cache trim whose cutoff is
// the given age before the pass.
func (h *reaperHarness) trimRecord(name string, cutoffAge time.Duration) {
	h.writeRecordNamed(name, autoGCRecord{At: h.now, OK: true, GoCaches: []goCacheTrim{{
		Cache: "shared go build cache", Root: "/var/cache/workspace/go-build",
		EstimatedBytes: 44 << 30, BudgetBytes: 40 << 30, YoungBytes: 14 << 30,
		Cutoff: h.now.Add(-cutoffAge), Removed: 71343, FreedBytes: 12 << 30,
		ShardsCovered: 256, ShardsTotal: 256, Finished: true,
	}}})
}

// Each pass states its cutoff, floor and counts at INFO and in the durable log.
func TestFloorAudit_ATrimRecordsTheFloor(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.run = func(context.Context, string) (reaperRun, error) {
		h.trimRecord(autoGCRecordName, 3*time.Hour)
		return reaperRun{}, nil
	}

	if !h.tick() || h.runs() != 1 {
		t.Fatalf("the pass must trim, runs=%d", h.runs())
	}
	for _, want := range []string{"level=INFO", "Go cache trimmed", "floor=2h0m0s", "cutoff=", "removed=71343", "forge="} {
		if !h.logged(want) {
			t.Errorf("INFO line is missing %q:\n%s", want, h.logs)
		}
	}
	if h.logged("floor_violated") || h.logged("level=ERROR") {
		t.Errorf("a cutoff 3h back is outside the floor:\n%s", h.logs)
	}
	a := h.audit()
	if len(a) != 1 {
		t.Fatalf("want one audit record, got %+v", a)
	}
	r := a[0]
	if r.Verdict != verdictTrimmed || r.Pass != "full" || r.Forge != version.Forge() || r.Floor != "2h0m0s" || !r.RecordFresh {
		t.Errorf("audit: %+v", r)
	}
	if len(r.Caches) != 1 || r.Caches[0].Removed != 71343 || r.Caches[0].Cutoff.IsZero() || r.Caches[0].CutoffAge != "3h0m0s" || r.Caches[0].FloorViolated {
		t.Errorf("audit cache: %+v", r.Caches)
	}
}

func TestFloorAudit_TheGoCachePassIsAudited(t *testing.T) {
	h, goRuns := goCacheHarness(t)
	h.writeRecordNamed(goCacheGCRecordName, autoGCRecord{At: h.now.Add(-30 * time.Minute), OK: true})
	h.writeForgeRecord(autoGCRecord{At: h.now.Add(-time.Minute), OK: true, CutOffLayers: []string{"go caches: shared build cache"}})
	h.tick()
	if len(*goRuns) != 1 {
		t.Fatalf("gocache runs=%d", len(*goRuns))
	}
	if a := h.audit(); len(a) != 1 || a[0].Pass != "gocache" || a[0].Verdict != verdictTrimmed || len(a[0].Caches) != 1 {
		t.Fatalf("audit: %+v", a)
	}
}

// Defence in depth: whatever forge claims to be, a trim whose cutoff lands
// inside the floor is reported as the defect it is.
func TestFloorAudit_ACutoffInsideTheFloorIsAnError(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.run = func(context.Context, string) (reaperRun, error) {
		h.trimRecord(autoGCRecordName, time.Hour+4*time.Minute) // the 03:14Z pass in the report
		return reaperRun{}, nil
	}
	h.tick()
	if !h.logged("level=ERROR") || !h.logged("floor_violated=true") {
		t.Fatalf("want an ERROR for a cutoff inside the floor:\n%s", h.logs)
	}
	if a := h.audit(); len(a) != 1 || !a[0].Caches[0].FloorViolated {
		t.Fatalf("audit: %+v", a)
	}
}

// Under budget pressure forge's size rule reaches past the 2h floor toward
// its 65-minute minimum (goCacheMinFloor): that is its contract, a WARN, not
// the ERROR every such pass logged while the audit knew only the 2h floor
// (#2762; the 2026-11-25 21:39Z pass, cutoff 1h7m).
func TestFloorAudit_ACutoffBetweenTheFloorsIsAWarning(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.run = func(context.Context, string) (reaperRun, error) {
		h.trimRecord(autoGCRecordName, time.Hour+7*time.Minute)
		return reaperRun{}, nil
	}
	h.tick()
	if h.logged("level=ERROR") || h.logged("floor_violated") {
		t.Fatalf("a cutoff past the minimum floor is not a violation:\n%s", h.logs)
	}
	if !h.logged("level=WARN") || !h.logged("inside_rest_floor=true") || !h.logged("min_floor=1h5m0s") {
		t.Fatalf("want a WARN naming both floors:\n%s", h.logs)
	}
	if a := h.audit(); len(a) != 1 || a[0].Caches[0].FloorViolated {
		t.Fatalf("audit: %+v", a)
	}
}

func TestTrimAuditRotatesAndNeverFailsAPass(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "forge", "storage.json")
	w := New(Options{noDocker: true, reaper: func(context.Context, string) (reaperRun, error) { return reaperRun{}, nil }})
	rec := trimAuditRecord{At: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), Verdict: verdictTrimmed, Forge: strings.Repeat("x", 1<<20)}
	for i := 0; i < trimAuditMaxBytes/(1<<20)+2; i++ {
		w.appendTrimAudit(context.Background(), policy, rec)
	}
	st, err := os.Stat(filepath.Join(filepath.Dir(policy), trimAuditName))
	if err != nil || st.Size() > trimAuditMaxBytes+(2<<20) {
		t.Fatalf("live audit log: %v size=%v", err, st)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(policy), trimAuditName+".1")); err != nil {
		t.Fatalf("the audit log was not rotated: %v", err)
	}
	// No policy location: nothing to write beside, and no panic.
	w.appendTrimAudit(context.Background(), "", rec)
}
