package lint

import (
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/pkg/release"
)

// An env a reconciler may apply is refused; one forge applies is only warned.
// The same object, the same rule, a different consequence.
func TestFluxSafeSeverityFollowsWhoAppliesTheEnv(t *testing.T) {
	f := release.FluxFinding{Rule: release.RuleJobTTL, Kind: "Job", Name: "temporal-schema-1023848b73"}
	if got := fluxSafeSeverity(fluxSafeFinding{reconciled: true, FluxFinding: f}); got != finding.SeverityError {
		t.Errorf("reconciled env: %s, want error", got)
	}
	if got := fluxSafeSeverity(fluxSafeFinding{reconciled: false, FluxFinding: f}); got != finding.SeverityWarning {
		t.Errorf("forge-applied env: %s, want warning", got)
	}
}

func TestFluxSafeLintIsRegisteredAsAGate(t *testing.T) {
	var found bool
	for _, s := range lintPipeline() {
		if s.name == "flux-safe lint" {
			found = true
			if !s.gates {
				t.Error("flux-safe lint does not gate: a TTL'd Job would pass `forge lint`")
			}
		}
	}
	if !found {
		t.Error("flux-safe lint is not in the sweep")
	}
}

// An env that moves onto the hub one delivery unit at a time is judged unit by
// unit: the same object is an ERROR in a unit the hub converges and a WARNING in
// one forge still applies, so the first unit to move is not held up by a Job in
// one that has not, and cannot carry its own.
func TestReconciledUnits_JudgesEachUnitByWhoAppliesIt(t *testing.T) {
	cases := []struct {
		name string
		r    reconciledUnits
		unit string
		want bool
	}{
		{"a plain real env: every unit", reconciledUnits{all: true}, "anything", true},
		{"a forge-applied env: no unit", reconciledUnits{}, "reliant", false},
		{"a unit the hub converges", reconciledUnits{units: map[string]bool{"reliant": true}}, "reliant", true},
		{"a unit forge still applies", reconciledUnits{units: map[string]bool{"reliant": true}}, "infra", false},
		{"the default unit, named", reconciledUnits{units: map[string]bool{"base": true}}, release.DefaultDeliveryUnit, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.applies(tc.unit); got != tc.want {
				t.Errorf("applies(%q) = %v, want %v", tc.unit, got, tc.want)
			}
		})
	}
}

// The finding carries its object's unit, which is what lets the lint grade it.
func TestCheckFluxSafety_FindingsCarryTheirUnit(t *testing.T) {
	doc := map[string]any{
		"kind": "Job",
		"metadata": map[string]any{
			"name":   "temporal-schema",
			"labels": map[string]any{release.LabelDeliveryUnit: "infra"},
		},
		"spec": map[string]any{"ttlSecondsAfterFinished": 86400},
	}
	fs := release.CheckFluxSafety([]any{doc})
	if len(fs) == 0 {
		t.Fatal("expected findings for a TTL'd, unhashed Job")
	}
	for _, f := range fs {
		if f.Unit != "infra" {
			t.Errorf("finding %s is in unit %q, want infra", f.Rule, f.Unit)
		}
	}
	delete(doc["metadata"].(map[string]any), "labels")
	for _, f := range release.CheckFluxSafety([]any{doc}) {
		if f.Unit != release.DefaultDeliveryUnit {
			t.Errorf("an object with no unit label is in %q, want the default unit", f.Unit)
		}
	}
}
