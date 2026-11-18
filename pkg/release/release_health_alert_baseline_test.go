package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func intPtr(n int) *int { return &n }

// alert_baseline_min_firings defaults to 1, recorded as absent, so a shape
// that keeps it encodes as it did before the field; 0 is a value of its own
// (count every alert), not the default.
func TestReleaseHealthAlertBaselineDefaults(t *testing.T) {
	h := DefaultReleaseHealth()
	if h.AlertBaselineFirings() != DefaultAlertBaselineMinFirings {
		t.Errorf("default = %d", h.AlertBaselineFirings())
	}
	if raw, _ := json.Marshal(h); strings.Contains(string(raw), "alert_baseline_min_firings") {
		t.Errorf("the default is encoded: %s", raw)
	}
	h.AlertBaselineMinFirings = intPtr(DefaultAlertBaselineMinFirings)
	if !h.IsDefault() {
		t.Error("the default value, set explicitly, is the default")
	}
	h.AlertBaselineMinFirings = intPtr(0)
	if h.IsDefault() || h.AlertBaselineFirings() != 0 {
		t.Errorf("0 is not the default: %d", h.AlertBaselineFirings())
	}
	if raw, _ := json.Marshal(h); !strings.Contains(string(raw), `"alert_baseline_min_firings":0`) {
		t.Errorf("0 is not encoded: %s", raw)
	}
	if err := h.validate(); err != nil {
		t.Errorf("validate = %v", err)
	}
	h.AlertBaselineMinFirings = intPtr(-1)
	if err := h.validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("-1: validate = %v", err)
	}
}

// A control plane that predates the field strict-decodes the shape, so it is
// sent the policy without it; when it was all that differed from the
// default, no policy at all.
func TestLimitShapeWithholdsAlertBaseline(t *testing.T) {
	old := &BundleReader{Features: []string{BundleFeatureReleaseHealth, BundleFeatureReleaseHealthBaseline, BundleFeatureReleaseHealthRegressedMinEvents, BundleFeatureReleaseHealthErrorRate}}
	cur := &BundleReader{Features: append(slicesOf(old.Features), BundleFeatureReleaseHealthAlertBaseline)}

	only := DefaultReleaseHealth()
	only.AlertBaselineMinFirings = intPtr(0)
	if got := old.LimitShape(Shape{ReleaseHealth: &only}).ReleaseHealth; got != nil {
		t.Errorf("alert-baseline-only policy to an old reader = %+v, want none", got)
	}
	if only.AlertBaselineMinFirings == nil {
		t.Error("LimitShape modified its argument")
	}
	if got := cur.LimitShape(Shape{ReleaseHealth: &only}).ReleaseHealth; got == nil || got.AlertBaselineFirings() != 0 {
		t.Errorf("current reader got %+v", got)
	}
	both := only
	both.MinRequests = 10
	if got := old.LimitShape(Shape{ReleaseHealth: &both}).ReleaseHealth; got == nil || got.AlertBaselineMinFirings != nil || got.MinRequests != 10 {
		t.Errorf("old reader with another field set got %+v", got)
	}
}
