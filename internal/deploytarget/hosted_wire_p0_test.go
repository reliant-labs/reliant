package deploytarget

import (
	"encoding/json"
	"testing"
)

// F0 mirrors P0's wire fields in this package's locally-declared structs
// too (§4.3). As in internal/cli, the test decodes documents written in the
// PROTO'S OWN proto3 JSON spelling rather than round-tripping forge's own
// types: a round-trip would pass even with every json tag misspelled, which
// is exactly the failure mode — the compiler is happy, and at runtime the
// field silently reads as the zero value.

// TestWireDeployment_DecodesTheReleaseBinding pins Deployment.artifact
// (§4.4 tag 12) and applied_promotion_id (tag 13).
//
// These two are what make the server's converger possible, and they are
// also what F1 sends. The artifact is the pairing key: EMPTY means not
// release-bound, and the converger skips such a row rather than guessing a
// pairing from the deployment's NAME — names coinciding with artifact keys
// is a convention, not a guarantee.
func TestWireDeployment_DecodesTheReleaseBinding(t *testing.T) {
	t.Parallel()
	const doc = `{
      "id": "dep_1",
      "name": "api",
      "tier": "DEPLOY_TIER_BACKEND",
      "runState": "DEPLOY_RUN_STATE_RUNNING",
      "artifact": "api",
      "appliedPromotionId": "pr_1",
      "observed": {"state": "DEPLOY_OBSERVED_STATE_READY", "imageDigest": "sha256:ab12", "replicas": 3}
    }`
	var w wireDeployment
	if err := json.Unmarshal([]byte(doc), &w); err != nil {
		t.Fatal(err)
	}
	if w.Artifact != "api" {
		t.Errorf("artifact did not decode: %q", w.Artifact)
	}
	if w.AppliedPromotionID != "pr_1" {
		t.Errorf("appliedPromotionId did not decode: %q", w.AppliedPromotionID)
	}
	// The pre-existing fields must keep decoding beside the new ones.
	if w.ID != "dep_1" || w.Name != "api" || w.Observed == nil || w.Observed.ImageDigest != "sha256:ab12" {
		t.Errorf("deployment = %+v", w)
	}

	// A row with NO artifact is the not-release-bound case (a database, a
	// pinned third-party image), and it must decode as empty rather than
	// as anything derived from the name.
	var unbound wireDeployment
	if err := json.Unmarshal([]byte(`{"id":"dep_2","name":"db","tier":"DEPLOY_TIER_DATABASE"}`), &unbound); err != nil {
		t.Fatal(err)
	}
	if unbound.Artifact != "" {
		t.Errorf("an absent artifact must stay empty, never default to the name; got %q", unbound.Artifact)
	}
	if unbound.AppliedPromotionID != "" {
		t.Errorf("an absent promotion must stay empty; got %q", unbound.AppliedPromotionID)
	}
}

// TestWireEnvironment_DecodesConvergesPromotions pins
// DeployEnvironment.converges_promotions (§4.4 tag 17).
//
// It is what lets a client refuse FAST. A `--wait` against an environment
// that converges nothing would otherwise poll for fifteen minutes and then
// report a failure whose cause is "nobody was ever going to apply this" —
// indistinguishable from a broken release.
func TestWireEnvironment_DecodesConvergesPromotions(t *testing.T) {
	t.Parallel()
	const doc = `{
      "id": "env_prod",
      "name": "prod",
      "project": "acme",
      "kind": "DEPLOY_ENVIRONMENT_KIND_PERSISTENT",
      "namespace": "acme-prod",
      "convergesPromotions": true
    }`
	var w wireEnvironment
	if err := json.Unmarshal([]byte(doc), &w); err != nil {
		t.Fatal(err)
	}
	if !w.ConvergesPromotions {
		t.Error("convergesPromotions did not decode")
	}
	if w.ID != "env_prod" || w.Project != "acme" {
		t.Errorf("environment = %+v", w)
	}

	// ABSENT MUST READ AS FALSE, not as true. A control plane that does
	// not send the field runs no converger, and defaulting to true would
	// make forge wait forever on an env that will never apply anything.
	var old wireEnvironment
	if err := json.Unmarshal([]byte(`{"id":"env_dev","name":"dev"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.ConvergesPromotions {
		t.Error("an absent convergesPromotions must read as false")
	}
}
