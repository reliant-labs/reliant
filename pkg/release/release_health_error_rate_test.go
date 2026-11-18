package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// error_rate_min_errors and baseline_window default to 10 and 24h, recorded
// as absent, so a shape that keeps them encodes as it did before the fields.
func TestReleaseHealthErrorRateDefaults(t *testing.T) {
	h := DefaultReleaseHealth()
	if h.ErrorMinErrors() != DefaultErrorRateMinErrors || h.BaselineWindow() != DefaultHealthBaselineWindow {
		t.Errorf("defaults = %d, %s", h.ErrorMinErrors(), h.BaselineWindow())
	}
	if raw, _ := json.Marshal(h); strings.Contains(string(raw), "error_rate_min_errors") || strings.Contains(string(raw), "baseline_window") {
		t.Errorf("the defaults are encoded: %s", raw)
	}
	h.ErrorRateMinErrors, h.BaselineWindowSeconds = DefaultErrorRateMinErrors, int(DefaultHealthBaselineWindow.Seconds())
	if !h.IsDefault() {
		t.Error("the default values, set explicitly, are the default")
	}
	h.ErrorRateMinErrors, h.BaselineWindowSeconds = 1, int((48 * time.Hour).Seconds())
	if h.IsDefault() || h.ErrorMinErrors() != 1 || h.BaselineWindow() != 48*time.Hour {
		t.Errorf("1 and 48h are not the defaults: %d %s", h.ErrorMinErrors(), h.BaselineWindow())
	}
	if err := h.validate(); err != nil {
		t.Errorf("validate = %v", err)
	}
	for name, bad := range map[string]ShapeReleaseHealth{
		"negative min errors":           func() ShapeReleaseHealth { b := h; b.ErrorRateMinErrors = -1; return b }(),
		"baseline shorter than window":  func() ShapeReleaseHealth { b := h; b.BaselineWindowSeconds = 60; return b }(),
		"baseline longer than the most": func() ShapeReleaseHealth { b := h; b.BaselineWindowSeconds = 8 * 24 * 3600; return b }(),
	} {
		if err := bad.validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: validate = %v", name, err)
		}
	}
}

// A control plane that predates the fields strict-decodes the shape, so it is
// sent the policy without them; when they were all that differed from the
// default, no policy at all.
func TestLimitShapeWithholdsErrorRateFields(t *testing.T) {
	old := &BundleReader{Features: []string{BundleFeatureReleaseHealth, BundleFeatureReleaseHealthBaseline, BundleFeatureReleaseHealthRegressedMinEvents}}
	cur := &BundleReader{Features: append(slicesOf(old.Features), BundleFeatureReleaseHealthErrorRate)}

	only := DefaultReleaseHealth()
	only.ErrorRateMinErrors, only.BaselineWindowSeconds = 1, 7200
	if got := old.LimitShape(Shape{ReleaseHealth: &only}).ReleaseHealth; got != nil {
		t.Errorf("error-rate-only policy to an old reader = %+v, want none", got)
	}
	if only.ErrorRateMinErrors != 1 {
		t.Error("LimitShape modified its argument")
	}
	if got := cur.LimitShape(Shape{ReleaseHealth: &only}).ReleaseHealth; got == nil || got.ErrorRateMinErrors != 1 || got.BaselineWindowSeconds != 7200 {
		t.Errorf("current reader got %+v", got)
	}
	both := only
	both.MinRequests = 10
	if got := old.LimitShape(Shape{ReleaseHealth: &both}).ReleaseHealth; got == nil || got.ErrorRateMinErrors != 0 || got.BaselineWindowSeconds != 0 || got.MinRequests != 10 {
		t.Errorf("old reader with another field set got %+v", got)
	}
}

func slicesOf(s []string) []string { return append([]string{}, s...) }
