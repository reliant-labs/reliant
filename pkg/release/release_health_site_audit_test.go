package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestSiteAuditCodesAreFindings: the site-audit codes are finding codes a
// policy may fail on, appended at the end (the order findings sort in), and
// the default policy fails on neither (design D2: warn-only by default).
func TestSiteAuditCodesAreFindings(t *testing.T) {
	n := len(HealthFindings)
	if !reflect.DeepEqual(HealthFindings[n-2:], []string{HealthSEORegressed, HealthA11yRegressed}) || HealthFindings[n-3] != HealthChangedOpLatency {
		t.Errorf("HealthFindings = %v: the site-audit codes must be appended at the end", HealthFindings)
	}
	if !reflect.DeepEqual(SiteAuditFindings, HealthFindings[n-2:]) {
		t.Errorf("SiteAuditFindings = %v", SiteAuditFindings)
	}
	if HealthSEORegressed != "seo_regressed" || HealthA11yRegressed != "a11y_regressed" {
		t.Error("the codes are a wire contract: seo_regressed, a11y_regressed")
	}
	def := DefaultReleaseHealth()
	for _, c := range SiteAuditFindings {
		if def.Fails(c) {
			t.Errorf("the default policy fails on %s; it warns", c)
		}
	}
	h := DefaultReleaseHealth()
	h.FailOn = append(h.FailOn, SiteAuditFindings...)
	if err := h.validate(); err != nil {
		t.Errorf("a policy failing on the site-audit codes: %v", err)
	}
}

// The score-drop thresholds default to 10, recorded as absent so a shape that
// keeps them encodes as it did before the fields; 0 (any drop) is a value.
func TestReleaseHealthScoreDropDefaults(t *testing.T) {
	h := DefaultReleaseHealth()
	if h.SEOScoreDrop() != DefaultSiteAuditMaxScoreDrop || h.A11yScoreDrop() != DefaultSiteAuditMaxScoreDrop || DefaultSiteAuditMaxScoreDrop != 10 {
		t.Errorf("defaults = %d, %d", h.SEOScoreDrop(), h.A11yScoreDrop())
	}
	if raw, _ := json.Marshal(h); strings.Contains(string(raw), "score_drop") {
		t.Errorf("the default is encoded: %s", raw)
	}
	h.SEOMaxScoreDrop, h.A11yMaxScoreDrop = intPtr(10), intPtr(10)
	if !h.IsDefault() {
		t.Error("the default value, set explicitly, is the default")
	}
	h.SEOMaxScoreDrop, h.A11yMaxScoreDrop = intPtr(0), intPtr(25)
	if h.IsDefault() || h.SEOScoreDrop() != 0 || h.A11yScoreDrop() != 25 {
		t.Errorf("0 and 25 are values: %d, %d", h.SEOScoreDrop(), h.A11yScoreDrop())
	}
	if raw, _ := json.Marshal(h); !strings.Contains(string(raw), `"seo_max_score_drop":0`) || !strings.Contains(string(raw), `"a11y_max_score_drop":25`) {
		t.Errorf("the values are not encoded: %s", raw)
	}
	if err := h.validate(); err != nil {
		t.Errorf("validate = %v", err)
	}
	for _, bad := range []int{-1, 101} {
		h.A11yMaxScoreDrop = intPtr(bad)
		if err := h.validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "a11y_max_score_drop") {
			t.Errorf("%d: validate = %v", bad, err)
		}
	}
}

// A control plane that predates the feature refuses a fail_on naming a code
// it does not know, and strict-decodes the thresholds: it is sent the policy
// without them, and none at all when they were all that differed from the
// default. A current reader gets the policy whole.
func TestLimitShapeWithholdsSiteAudit(t *testing.T) {
	old := &BundleReader{Features: slices.DeleteFunc(BundleDocFeatures(), func(f string) bool { return f == BundleFeatureReleaseHealthSiteAudit })}
	full := Shape{Kind: EnvSelfManaged, ReleaseHealth: func() *ShapeReleaseHealth {
		h := DefaultReleaseHealth()
		h.MinRequests = 20
		h.FailOn = append(slices.Clone(h.FailOn), HealthSEORegressed)
		h.SEOMaxScoreDrop, h.A11yMaxScoreDrop = intPtr(5), intPtr(0)
		return &h
	}()}
	got := old.LimitShape(full)
	if got.ReleaseHealth == nil || got.ReleaseHealth.MinRequests != 20 {
		t.Fatalf("the rest of the policy was lost: %+v", got.ReleaseHealth)
	}
	if slices.Contains(got.ReleaseHealth.FailOn, HealthSEORegressed) || got.ReleaseHealth.SEOMaxScoreDrop != nil || got.ReleaseHealth.A11yMaxScoreDrop != nil {
		t.Errorf("an old reader was sent the site-audit policy: %+v", got.ReleaseHealth)
	}
	if !reflect.DeepEqual(got.ReleaseHealth.FailOn, DefaultReleaseHealth().FailOn) {
		t.Errorf("fail_on = %v, want the default", got.ReleaseHealth.FailOn)
	}
	if err := got.ReleaseHealth.validate(); err != nil {
		t.Errorf("what an old reader is sent: %v", err)
	}
	if !slices.Contains(full.ReleaseHealth.FailOn, HealthSEORegressed) || full.ReleaseHealth.SEOMaxScoreDrop == nil {
		t.Fatal("LimitShape modified the shape it was given")
	}
	if !full.HasOptionalParts() {
		t.Error("a shape with site-audit policy reports no optional parts, so its reader's features are never asked")
	}
	raw, err := json.Marshal(got.ReleaseHealth)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"seo_max_score_drop", "a11y_max_score_drop", HealthSEORegressed} {
		if strings.Contains(string(raw), key) {
			t.Errorf("an old reader's policy names %s: %s", key, raw)
		}
	}

	for name, h := range map[string]func(*ShapeReleaseHealth){
		"only a code":      func(h *ShapeReleaseHealth) { h.FailOn = append(slices.Clone(h.FailOn), HealthA11yRegressed) },
		"only a threshold": func(h *ShapeReleaseHealth) { h.A11yMaxScoreDrop = intPtr(3) },
	} {
		only := DefaultReleaseHealth()
		h(&only)
		if got := old.LimitShape(Shape{Kind: EnvSelfManaged, ReleaseHealth: &only}); got.ReleaseHealth != nil {
			t.Errorf("%s: a policy that differed only by site audit: sent %+v, want the default (absent)", name, got.ReleaseHealth)
		}
	}

	for name, r := range map[string]*BundleReader{
		"every feature": {Features: BundleDocFeatures()},
		"nil reader":    nil,
	} {
		if got := r.LimitShape(full); !reflect.DeepEqual(got, full) {
			t.Errorf("%s: LimitShape changed the shape:\n got %+v\nwant %+v", name, got, full)
		}
	}
}
