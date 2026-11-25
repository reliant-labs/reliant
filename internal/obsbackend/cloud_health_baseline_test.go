package obsbackend

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// forge report 9583ed60: the cloud decoder dropped findings[].unattributed and
// baselineAlerts, so a check that re-judged the control plane's answer
// (--fail-on) failed a release on an alert the control plane had already
// found flapping before it, and the report never listed the baseline alerts.
func TestReportFromWire_CarriesBaselineAlertsAndUnattributed(t *testing.T) {
	var w wireReleaseHealth
	if err := json.Unmarshal([]byte(`{"promotion":{"releaseVersion":"v2","promotedAt":"2026-11-11T12:00:00Z"},
		"verdict":"RELEASE_VERDICT_PASS",
		"baselineAlerts":[{"rule":"HighErrorRate","fingerprint":"fp1","firingAt":"2026-11-11T12:05:00Z","baselineFirings":4}],
		"findings":[{"code":"RELEASE_FINDING_CODE_ALERT_FIRING","policy":"warn","message":"HighErrorRate fired",
			"unattributed":"the same alert fired 4 time(s) before the promotion"}]}`), &w); err != nil {
		t.Fatal(err)
	}
	r, err := reportFromWire(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.BaselineAlerts) != 1 || r.BaselineAlerts[0].Rule != "HighErrorRate" || r.BaselineAlerts[0].BaselineFirings != 4 {
		t.Errorf("baseline alerts = %+v", r.BaselineAlerts)
	}
	if len(r.Findings) != 1 || r.Findings[0].Unattributed == "" {
		t.Fatalf("findings = %+v", r.Findings)
	}
	// What the field is for: re-judging with alert_firing in the fail-on set
	// keeps an unattributed finding at warn.
	if got := releasehealth.Rejudge(r, []string{"alert_firing"}); got.Findings[0].Policy != "warn" {
		t.Errorf("an unattributed finding was re-judged %q", got.Findings[0].Policy)
	}
}
