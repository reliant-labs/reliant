package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// regressed_issue_min_events follows new_issue_min_events unless set to
// something else, and that default is recorded as absent, so a shape that
// keeps it encodes as it did before the field.
func TestReleaseHealthRegressedMinEventsDefault(t *testing.T) {
	h := DefaultReleaseHealth()
	if h.RegressedIssueMinEvents != 0 || h.RegressedMinEvents() != h.NewIssueMinEvents {
		t.Errorf("default = %d (%d)", h.RegressedIssueMinEvents, h.RegressedMinEvents())
	}
	if raw, _ := json.Marshal(h); strings.Contains(string(raw), "regressed_issue_min_events") {
		t.Errorf("the default is encoded: %s", raw)
	}
	h.RegressedIssueMinEvents = h.NewIssueMinEvents
	if !h.IsDefault() {
		t.Error("a value equal to new_issue_min_events is the default")
	}
	h.RegressedIssueMinEvents = 1
	if h.IsDefault() || h.RegressedMinEvents() != 1 {
		t.Errorf("1 is not the default: %d", h.RegressedMinEvents())
	}
	if err := h.validate(); err != nil {
		t.Errorf("validate(1) = %v", err)
	}
	h.RegressedIssueMinEvents = -1
	if err := h.validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("validate(-1) = %v", err)
	}
}

// A control plane that predates regressed_issue_min_events strict-decodes the
// shape, so it is sent the policy without it; when it was all that differed
// from the default, no policy at all. require_baseline is withheld beside it
// by its own feature.
func TestLimitShapeWithholdsRegressedMinEvents(t *testing.T) {
	old := &BundleReader{Features: []string{BundleFeatureReleaseHealth, BundleFeatureReleaseHealthBaseline}}
	cur := &BundleReader{Features: []string{BundleFeatureReleaseHealth, BundleFeatureReleaseHealthBaseline, BundleFeatureReleaseHealthRegressedMinEvents}}

	only := DefaultReleaseHealth()
	only.RegressedIssueMinEvents = 1
	if got := old.LimitShape(Shape{ReleaseHealth: &only}).ReleaseHealth; got != nil {
		t.Errorf("regressed-only policy to an old reader = %+v, want none", got)
	}
	if only.RegressedIssueMinEvents != 1 {
		t.Error("LimitShape modified its argument")
	}
	if got := cur.LimitShape(Shape{ReleaseHealth: &only}).ReleaseHealth; got == nil || got.RegressedIssueMinEvents != 1 {
		t.Errorf("current reader got %+v", got)
	}

	both := only
	both.RequireBaseline = BaselineEvery
	both.MinRequests = 10
	oldest := &BundleReader{Features: []string{BundleFeatureReleaseHealth}}
	if got := oldest.LimitShape(Shape{ReleaseHealth: &both}).ReleaseHealth; got == nil || got.RegressedIssueMinEvents != 0 || got.RequireBaseline != "" || got.MinRequests != 10 {
		t.Errorf("a reader with neither feature got %+v", got)
	}
	if got := old.LimitShape(Shape{ReleaseHealth: &both}).ReleaseHealth; got == nil || got.RegressedIssueMinEvents != 0 || got.RequireBaseline != BaselineEvery {
		t.Errorf("a reader with the baseline feature only got %+v", got)
	}
}
