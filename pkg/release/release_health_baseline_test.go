package release

import (
	"errors"
	"testing"
)

// The default baseline policy is recorded as absent, so a shape that keeps it
// encodes as it did before the field.
func TestReleaseHealthBaselineDefault(t *testing.T) {
	h := DefaultReleaseHealth()
	if h.RequireBaseline != "" || h.Baseline() != BaselineAny {
		t.Errorf("default = %q (%q)", h.RequireBaseline, h.Baseline())
	}
	h.RequireBaseline = BaselineAny
	if !h.IsDefault() {
		t.Error("an explicit \"any\" is the default")
	}
	h.RequireBaseline = BaselineEvery
	if h.IsDefault() {
		t.Error("\"every\" is not the default")
	}
	h.RequireBaseline = "some"
	if err := h.validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("validate(%q) = %v", h.RequireBaseline, err)
	}
}

// A control plane that predates require_baseline strict-decodes the shape, so
// it is sent the policy without it; when the baseline was all that differed
// from the default, no policy at all.
func TestLimitShapeWithholdsRequireBaseline(t *testing.T) {
	old := &BundleReader{Features: []string{BundleFeatureReleaseHealth}}
	cur := &BundleReader{Features: []string{BundleFeatureReleaseHealth, BundleFeatureReleaseHealthBaseline}}

	only := DefaultReleaseHealth()
	only.RequireBaseline = BaselineEvery
	if got := old.LimitShape(Shape{ReleaseHealth: &only}).ReleaseHealth; got != nil {
		t.Errorf("baseline-only policy to an old reader = %+v, want none", got)
	}
	if only.RequireBaseline != BaselineEvery {
		t.Error("LimitShape modified its argument")
	}

	more := only
	more.MinRequests = 10
	got := old.LimitShape(Shape{ReleaseHealth: &more}).ReleaseHealth
	if got == nil || got.RequireBaseline != "" || got.MinRequests != 10 {
		t.Errorf("old reader got %+v", got)
	}
	if got := cur.LimitShape(Shape{ReleaseHealth: &more}).ReleaseHealth; got == nil || got.RequireBaseline != BaselineEvery {
		t.Errorf("current reader got %+v", got)
	}
}
