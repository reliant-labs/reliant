package release

import (
	"reflect"
	"slices"
	"testing"
)

// TestChangedOpCodesAreFindings: the changed-op codes are finding codes a
// policy may fail on, appended after the existing ones (the order findings
// sort in), and the default policy fails on neither.
func TestChangedOpCodesAreFindings(t *testing.T) {
	n := len(HealthFindings)
	if !reflect.DeepEqual(HealthFindings[n-2:], []string{HealthChangedOpErrors, HealthChangedOpLatency}) || HealthFindings[n-3] != HealthProbeLatency {
		t.Errorf("HealthFindings = %v: the changed-op codes must be appended at the end", HealthFindings)
	}
	if !reflect.DeepEqual(ChangedOpFindings, HealthFindings[n-2:]) {
		t.Errorf("ChangedOpFindings = %v", ChangedOpFindings)
	}
	def := DefaultReleaseHealth()
	for _, c := range ChangedOpFindings {
		if def.Fails(c) {
			t.Errorf("the default policy fails on %s; it warns", c)
		}
	}
	h := DefaultReleaseHealth()
	h.FailOn = append(h.FailOn, HealthChangedOpErrors, HealthChangedOpLatency)
	if err := h.validate(); err != nil {
		t.Errorf("a policy failing on the changed-op codes: %v", err)
	}
}

// TestLimitShapeWithholdsChangedOpCodesFromAnOldReader: a control plane that
// predates the changed-op codes refuses a fail_on naming one, and with it the
// whole shape, so it is sent the policy without them; a policy that differed
// from the default only by them is sent as the default (absent).
func TestLimitShapeWithholdsChangedOpCodesFromAnOldReader(t *testing.T) {
	// Every feature but this one: what the changed-op codes alone need.
	old := &BundleReader{Features: slices.DeleteFunc(BundleDocFeatures(), func(f string) bool { return f == BundleFeatureReleaseHealthChangedOps })}

	health := DefaultReleaseHealth()
	health.WindowSeconds = 1800
	health.FailOn = append(slices.Clone(health.FailOn), HealthChangedOpErrors, HealthLatency)
	full := Shape{Kind: EnvSelfManaged, ReleaseHealth: &health}

	got := old.LimitShape(full)
	if got.ReleaseHealth == nil {
		t.Fatal("the release-health policy was dropped with the changed-op codes")
	}
	want := append(slices.Clone(DefaultReleaseHealth().FailOn), HealthLatency)
	if !reflect.DeepEqual(got.ReleaseHealth.FailOn, want) || got.ReleaseHealth.WindowSeconds != 1800 {
		t.Errorf("old reader's policy = %+v, want fail_on %v and the declared window", got.ReleaseHealth, want)
	}
	if err := got.ReleaseHealth.validate(); err != nil {
		t.Errorf("what an old reader is sent: %v", err)
	}
	if !slices.Contains(full.ReleaseHealth.FailOn, HealthChangedOpErrors) {
		t.Fatal("LimitShape modified the shape it was given")
	}
	if !full.HasOptionalParts() {
		t.Error("a shape failing on a changed-op code reports no optional parts, so its reader's features are never asked")
	}

	only := DefaultReleaseHealth()
	only.FailOn = append(slices.Clone(only.FailOn), HealthChangedOpLatency)
	if got := old.LimitShape(Shape{Kind: EnvSelfManaged, ReleaseHealth: &only}); got.ReleaseHealth != nil {
		t.Errorf("a policy that differed only by a changed-op code: sent %+v, want the default (absent)", got.ReleaseHealth)
	}

	for name, r := range map[string]*BundleReader{
		"every feature": {Features: BundleDocFeatures()},
		"nil reader":    nil,
	} {
		if got := r.LimitShape(full); !reflect.DeepEqual(got, full) {
			t.Errorf("%s: LimitShape changed the shape:\n got %+v\nwant %+v", name, got, full)
		}
	}

	// A policy without the codes needs nothing of the reader.
	plain := DefaultReleaseHealth()
	plain.WindowSeconds = 1800
	if got := old.LimitShape(Shape{Kind: EnvSelfManaged, ReleaseHealth: &plain}); !reflect.DeepEqual(got.ReleaseHealth, &plain) {
		t.Errorf("a policy without changed-op codes was limited: %+v", got.ReleaseHealth)
	}
}
