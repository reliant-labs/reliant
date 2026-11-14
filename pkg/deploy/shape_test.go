package deploy

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

const gib = int64(1) << 30

func TestCheckShapeBand(t *testing.T) {
	conformant := []v1alpha1.Resources{
		{}, // defaults: the 250m / 1Gi entry shape
		{CPURequestMillicores: 125, MemoryRequestBytes: gib / 2, CPULimitMillicores: 125, MemoryLimitBytes: gib / 2},
		{CPURequestMillicores: 500, MemoryRequestBytes: 2 * gib, CPULimitMillicores: 2000, MemoryLimitBytes: 4 * gib},
		{CPURequestMillicores: 1000, MemoryRequestBytes: 4 * gib, CPULimitMillicores: 1000, MemoryLimitBytes: 4 * gib},
		{CPURequestMillicores: 4000, MemoryRequestBytes: 16 * gib, CPULimitMillicores: 4000, MemoryLimitBytes: 16 * gib},
	}
	for _, r := range conformant {
		if err := CheckShapeBand(r); err != nil {
			t.Errorf("%+v should be on the band: %v", r, err)
		}
	}

	lopsided := []struct {
		r    v1alpha1.Resources
		want string
	}{
		// The live sb-api3 shape that bypassed the KCL-only check.
		{v1alpha1.Resources{CPURequestMillicores: 100, MemoryRequestBytes: 128 << 20}, "multiple of 125m"},
		{v1alpha1.Resources{CPURequestMillicores: 1000, MemoryRequestBytes: gib}, "needs exactly 4294967296 bytes (4Gi)"},
		{v1alpha1.Resources{CPURequestMillicores: 250, MemoryRequestBytes: 4 * gib, MemoryLimitBytes: 4 * gib}, "off the hosted 4 GiB-per-vCPU shape band"},
	}
	for _, c := range lopsided {
		err := CheckShapeBand(c.r)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: want reason containing %q, got %v", c.r, c.want, err)
		}
	}
}

// TestShapeBandOnDefaultedRequestOnlySpec: the hosted path runs Validate AND
// CheckShapeBand on the same spec. A request-only block on the band (the
// common way to size up) must pass both, with the band judged on the
// defaulted values and the limits defaulting to the requests.
func TestShapeBandOnDefaultedRequestOnlySpec(t *testing.T) {
	for _, r := range []v1alpha1.Resources{
		{CPURequestMillicores: 500, MemoryRequestBytes: 2 * gib},
		{CPURequestMillicores: 250}, // memory request defaults to 1Gi: on the band
		{MemoryRequestBytes: gib},   // cpu request defaults to 250m: on the band
		{CPURequestMillicores: 1000, MemoryRequestBytes: 4 * gib, MemoryLimitBytes: 8 * gib},
	} {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: Validate: %v", r, err)
		}
		if err := CheckShapeBand(r); err != nil {
			t.Errorf("%+v: CheckShapeBand: %v", r, err)
		}
	}
	// Judged on the DEFAULTED request: 500m with memory left to the 1Gi
	// default is off the band, and the reason says so.
	if err := CheckShapeBand(v1alpha1.Resources{CPURequestMillicores: 500}); err == nil || !strings.Contains(err.Error(), "500m needs exactly 2147483648 bytes") {
		t.Errorf("500m with defaulted memory: want off-band, got %v", err)
	}
}

// TestRatioFloorMatchesControlPlane pins the worked examples from
// control-plane's inframeter.RatioFloor doc, converted to bytes.
func TestRatioFloorMatchesControlPlane(t *testing.T) {
	cases := []struct{ cpu, mem, wantCPU, wantMem int64 }{
		{500, 8 * gib, 2000, 8 * gib},  // brief's example: memory-heavy bills 2 vCPU
		{1000, gib, 1000, 4 * gib},     // CPU-heavy bills 4 GiB
		{1000, 4 * gib, 1000, 4 * gib}, // on the band: unchanged
		{250, gib + 1, 251, gib + 1},   // one byte over rounds CPU UP, never down
	}
	for _, c := range cases {
		cpu, mem := RatioFloor(c.cpu, c.mem)
		if cpu != c.wantCPU || mem != c.wantMem {
			t.Errorf("RatioFloor(%d, %d) = (%d, %d), want (%d, %d)", c.cpu, c.mem, cpu, mem, c.wantCPU, c.wantMem)
		}
	}
	// Every band-conformant shape is a fixed point of the floor: the two
	// halves of the rule agree by construction.
	for cpu := int64(125); cpu <= 8000; cpu += 125 {
		if c, m := RatioFloor(cpu, BandMemoryBytes(cpu)); c != cpu || m != BandMemoryBytes(cpu) {
			t.Fatalf("on-band shape %dm moved under RatioFloor to (%d, %d)", cpu, c, m)
		}
	}
}

func TestObservedStateMapping(t *testing.T) {
	want := map[v1alpha1.Phase]ObservedState{
		v1alpha1.PhasePending: ObservedPending, v1alpha1.PhaseProgressing: ObservedProgressing,
		v1alpha1.PhaseReady: ObservedReady, v1alpha1.PhaseDegraded: ObservedDegraded,
		v1alpha1.PhaseFailed: ObservedDegraded, v1alpha1.PhaseLocked: ObservedDeleted,
		"": ObservedUnknown, "Weird": ObservedUnknown,
	}
	for p, w := range want {
		if got := ObservedStateOf(p); got != w {
			t.Errorf("ObservedStateOf(%q) = %q, want %q", p, got, w)
		}
	}

	now := time.Unix(1_700_000_000, 0)
	if !Converged(v1alpha1.PhaseReady, now.Add(-3*time.Minute), now, DefaultStabilityWindow) {
		t.Error("ready for longer than the window must be converged")
	}
	if Converged(v1alpha1.PhaseReady, now.Add(-30*time.Second), now, DefaultStabilityWindow) {
		t.Error("ready but inside the window must NOT be converged (the instant-ready definition this replaces)")
	}
	if Converged(v1alpha1.PhaseReady, time.Time{}, now, DefaultStabilityWindow) {
		t.Error("an unknown transition time must not count as converged")
	}
	if Converged(v1alpha1.PhaseDegraded, now.Add(-time.Hour), now, DefaultStabilityWindow) {
		t.Error("degraded is never converged")
	}
}
