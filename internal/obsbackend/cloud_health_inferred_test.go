package obsbackend

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// A control plane that judged a version no promotion carried says so, and the
// report carries the label to every rendering.
func TestReportFromWire_CarriesTheInferredMarker(t *testing.T) {
	var w wireReleaseHealth
	if err := json.Unmarshal([]byte(`{"promotion":{"releaseVersion":"v2","promotedAt":"2026-11-11T12:00:00Z",
		"previousReleaseVersion":"v1","inferred":true,"markerSource":"telemetry_first_seen"},"verdict":"RELEASE_VERDICT_PASS"}`), &w); err != nil {
		t.Fatal(err)
	}
	r, err := reportFromWire(w)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Subject.Inferred || r.Subject.MarkerSource != releasehealth.MarkerFirstSeen || r.Subject.PromotionID != "" {
		t.Fatalf("subject = %+v", r.Subject)
	}
	// An older control plane says nothing: a recorded promotion.
	var old wireReleaseHealth
	if err := json.Unmarshal([]byte(`{"promotion":{"id":"p1","releaseVersion":"v2"},"verdict":"RELEASE_VERDICT_PASS"}`), &old); err != nil {
		t.Fatal(err)
	}
	if r, _ = reportFromWire(old); r.Subject.Inferred {
		t.Fatal("a promotion read as inferred")
	}
}
