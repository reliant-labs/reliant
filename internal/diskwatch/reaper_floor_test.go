// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"
)

const (
	// The forge the daemon embedded on 2026-10-22 (reliant 0293360df8b4):
	// before forge#1231, #1243 and #1245.
	staleForge = "v0.1.47-0.20261022211527-ce1870c66476"
	// The forge the daemon was updated to at 2026-10-26T03:43Z (reliant
	// 793a0a8a5fd4): it has all three.
	fixedForge = "v0.1.47-0.20261025050757-0436d3610f38"
)

func (h *reaperHarness) forge(v string) {
	h.t.Helper()
	h.w.reaper.forgeVersion = func() string { return v }
}

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

// The 2026-10-26 incident: the daemon's embedded forge predates the floor and
// the cache is over budget on nearly every pass. The full pass (which trims
// the Go caches first) must not run.
func TestFloorGuard_StaleForgeSkipsTheFullPass(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.forge(staleForge)

	if !h.tick() {
		t.Fatal("the pass was due and should have been attempted (and refused)")
	}
	if h.runs() != 0 {
		t.Fatalf("a forge that predates the floor must not trim: %d run(s)", h.runs())
	}
	if !h.logged("level=WARN") || !h.logged("predates the 2h live-build floor") || !h.logged(staleForge) {
		t.Fatalf("want a WARN naming the forge and the floor:\n%s", h.logs)
	}
	a := h.audit()
	if len(a) != 1 || a[0].Verdict != verdictSkippedStaleForge || a[0].Pass != "full" || a[0].Forge != staleForge || a[0].MinForge != MinFloorForge {
		t.Fatalf("want one skipped record in the durable log, got %+v", a)
	}

	// A refused pass is stamped like any attempt: it must not be retried on
	// the next one-minute poll.
	h.advance(time.Minute)
	if h.tick() {
		t.Fatal("a skipped pass was retried a minute later")
	}
}

func TestFloorGuard_StaleForgeSkipsTheGoCachePass(t *testing.T) {
	h, goRuns := goCacheHarness(t)
	h.forge(staleForge)
	h.writeRecordNamed(goCacheGCRecordName, autoGCRecord{At: h.now.Add(-30 * time.Minute), OK: true})
	h.writeForgeRecord(autoGCRecord{At: h.now.Add(-time.Minute), OK: true, CutOffLayers: []string{"go caches: shared build cache"}})

	if !h.tick() {
		t.Fatal("the Go-cache pass was due")
	}
	if len(*goRuns) != 0 || h.runs() != 0 {
		t.Fatalf("a stale forge trimmed: gocache=%d full=%d", len(*goRuns), h.runs())
	}
	if !h.logged("predates the 2h live-build floor") {
		t.Fatalf("no WARN:\n%s", h.logs)
	}
	if a := h.audit(); len(a) != 1 || a[0].Verdict != verdictSkippedStaleForge || a[0].Pass != "gocache" {
		t.Fatalf("audit: %+v", a)
	}
}

func TestFloorGuard_UnidentifiableForgeIsTreatedAsStale(t *testing.T) {
	for _, v := range []string{"unknown", "(devel)", ""} {
		h := newReaperHarness(t, plentyOfSpace)
		h.forge(v)
		h.tick()
		if h.runs() != 0 {
			t.Errorf("forge version %q: trimmed without proof of the floor", v)
		}
	}
}

// A full disk is worse than an unsafe trim: below the emergency threshold the
// old policy is allowed, loudly.
func TestFloorGuard_EmergencyStillTrimsAndSaysSo(t *testing.T) {
	h := newReaperHarness(t, 0) // no free space on the build-cache volume
	h.forge(staleForge)
	h.run = func(context.Context, string) (reaperRun, error) {
		h.trimRecord(autoGCRecordName, 20*time.Minute)
		return reaperRun{}, nil
	}

	if !h.tick() || h.runs() != 1 {
		t.Fatalf("an emergency must still trim, runs=%d", h.runs())
	}
	if !h.logged("level=ERROR") || !h.logged("emergency") || !h.logged(staleForge) {
		t.Fatalf("want a loud ERROR naming the emergency and the forge:\n%s", h.logs)
	}
	a := h.audit()
	if len(a) != 1 || a[0].Verdict != verdictEmergencyStaleForge || !a[0].Emergency || a[0].EmergencyBelowBytes == 0 {
		t.Fatalf("audit: %+v", a)
	}
	if len(a[0].Caches) != 1 || !a[0].Caches[0].FloorViolated {
		t.Fatalf("a 20m cutoff must be recorded as inside the floor: %+v", a[0].Caches)
	}
}

func TestFloorGuard_PlentyOfSpaceIsNoEmergency(t *testing.T) {
	// Free space below the threshold on a plentiful cache volume is no
	// emergency: the test machine's cache volume has 30 GiB free.
	h := newReaperHarness(t, plentyOfSpace)
	h.forge(staleForge)
	h.tick()
	if h.runs() != 0 {
		t.Fatal("trimmed without an emergency")
	}
	if a := h.audit(); len(a) != 1 || a[0].Emergency {
		t.Fatalf("audit: %+v", a)
	}
}

// The fixed forge trims normally, and each pass states its cutoff, floor and
// counts at INFO and in the durable log.
func TestFloorGuard_FixedForgeTrimsAndRecordsTheFloor(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.forge(fixedForge)
	h.run = func(context.Context, string) (reaperRun, error) {
		h.trimRecord(autoGCRecordName, 3*time.Hour)
		return reaperRun{}, nil
	}

	if !h.tick() || h.runs() != 1 {
		t.Fatalf("a forge with the floor must trim, runs=%d", h.runs())
	}
	for _, want := range []string{"level=INFO", "Go cache trimmed", "floor=2h0m0s", "cutoff=", "removed=71343", "forge=" + fixedForge} {
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
	if r.Verdict != verdictTrimmed || r.Pass != "full" || r.Forge != fixedForge || r.Floor != "2h0m0s" || !r.RecordFresh {
		t.Errorf("audit: %+v", r)
	}
	if len(r.Caches) != 1 || r.Caches[0].Removed != 71343 || r.Caches[0].Cutoff.IsZero() || r.Caches[0].CutoffAge != "3h0m0s" || r.Caches[0].FloorViolated {
		t.Errorf("audit cache: %+v", r.Caches)
	}
}

func TestFloorGuard_FixedForgeGoCachePassIsAudited(t *testing.T) {
	h, goRuns := goCacheHarness(t)
	h.forge(fixedForge)
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
func TestFloorGuard_ACutoffInsideTheFloorIsAnError(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.forge(fixedForge)
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

func TestForgeHasFloor(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{staleForge, false},
		{"v0.1.46", false},
		{"v0.1.47-0.20261024013900-000000000000", false}, // a second before #1245
		{MinFloorForge, true},
		{fixedForge, true},
		{"v0.1.47-0.20261025231900-52b88f2e98f3", true}, // main's pin on 2026-10-26
		{"v0.1.47", true},
		{"v0.2.0", true},
		{"unknown", false},
		{"(devel)", false},
		{"", false},
	} {
		got, why := forgeHasFloor(tc.v)
		if got != tc.want {
			t.Errorf("forgeHasFloor(%q) = %v (%s), want %v", tc.v, got, why, tc.want)
		}
		if !got && why == "" {
			t.Errorf("forgeHasFloor(%q) refused without a reason", tc.v)
		}
	}
}

// MinFloorForge is bumped alongside forge's pin: this binary must itself
// carry a forge at or above it, or the daemon built from this tree would
// refuse every Go-cache trim.
func TestMinFloorForgeIsNotAboveThisModulesForgePin(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*github\.com/reliant-labs/forge\s+(v\S+)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("go.mod has no forge requirement")
	}
	pin := string(m[1])
	if !semver.IsValid(MinFloorForge) {
		t.Fatalf("MinFloorForge %q is not a valid version", MinFloorForge)
	}
	if ok, why := forgeHasFloor(pin); !ok {
		t.Fatalf("go.mod pins forge %s, which %s: bump the pin or lower MinFloorForge only if the floor is in it", pin, why)
	}
}

func TestTrimAuditRotatesAndNeverFailsAPass(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "forge", "storage.json")
	w := New(Options{noDocker: true, reaper: func(context.Context, string) (reaperRun, error) { return reaperRun{}, nil }})
	rec := trimAuditRecord{At: time.Now(), Verdict: verdictTrimmed, Reason: strings.Repeat("x", 1<<20)}
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
