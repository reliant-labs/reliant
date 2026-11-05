package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// F0 mirrors EVERY P0 wire field in forge's locally-declared structs (§4.3:
// "a missing wire field is an F0 follow-up"). forge imports no control-plane
// protos, so nothing but a test can catch a field that was never mirrored —
// the compiler is happy either way, and the symptom at runtime is a value
// that silently reads as the zero value.
//
// So these tests decode documents written in the PROTO'S OWN proto3 JSON
// spelling, literally, rather than marshalling forge's structs and decoding
// them back. A round-trip through forge's own types would pass even if every
// json tag were misspelled, which is exactly the bug class at issue.

// TestWirePromotion_DecodesEveryP0Field pins the §4.4 tag allocation for
// DeployPromotion: recordedGates (14), supersededInFlight (15),
// fromEnvironmentName (16), fromPromotionId (17), run (18).
func TestWirePromotion_DecodesEveryP0Field(t *testing.T) {
	t.Parallel()
	const doc = `{
      "id": "pr_1",
      "environmentId": "env_prod",
      "releaseId": "rel_1",
      "releaseVersion": "v1.4.0",
      "kind": "DEPLOY_PROMOTION_KIND_PROMOTE",
      "fromEnvironmentId": "env_staging",
      "fromEnvironmentName": "staging",
      "fromPromotionId": "pr_source",
      "supersededInFlight": true,
      "resolvedArtifacts": {"api": "sha256:aa"},
      "promotedByUserId": "usr_1",
      "promotedByActor": "ci",
      "note": "hotfix",
      "createdAt": "2026-10-01T00:00:00Z",
      "run": {"id": "github:acme/app/12345/2", "url": "https://gh/run/12345", "provider": "github"},
      "gates": [{"name": "lint", "status": "passed", "url": "https://ci/lint"}],
      "recordedGates": [{
        "name": "smoke", "status": "failed", "url": "https://ci/smoke",
        "summary": "3 of 4 endpoints responded",
        "startedAt": "2026-10-01T00:01:00Z",
        "finishedAt": "2026-10-01T00:02:00Z",
        "runId": "github:acme/app/12345/2",
        "details": {"passed": 3, "failed": 1},
        "recordedBy": "token:tok_1",
        "recordedAt": "2026-10-01T00:02:05Z"
      }]
    }`
	var w wirePromotion
	if err := json.Unmarshal([]byte(doc), &w); err != nil {
		t.Fatal(err)
	}

	if w.FromEnvironmentName != "staging" {
		t.Errorf("fromEnvironmentName did not decode: %q", w.FromEnvironmentName)
	}
	if w.FromPromotionID != "pr_source" {
		t.Errorf("fromPromotionId did not decode: %q", w.FromPromotionID)
	}
	if !w.SupersededInFlight {
		t.Error("supersededInFlight did not decode")
	}
	if w.Run == nil || w.Run.ID != "github:acme/app/12345/2" || w.Run.Provider != "github" {
		t.Errorf("run did not decode: %+v", w.Run)
	}
	if len(w.RecordedGates) != 1 {
		t.Fatalf("recordedGates did not decode: %+v", w.RecordedGates)
	}

	// Every DeployGate field (§4.4 tags 4–10).
	g := w.RecordedGates[0]
	if g.Summary != "3 of 4 endpoints responded" {
		t.Errorf("gate summary did not decode: %q", g.Summary)
	}
	if g.StartedAt == nil || g.FinishedAt == nil {
		t.Errorf("gate timing did not decode: %+v %+v", g.StartedAt, g.FinishedAt)
	}
	if g.RunID != "github:acme/app/12345/2" {
		t.Errorf("gate runId did not decode: %q", g.RunID)
	}
	if g.Details["failed"] != float64(1) {
		t.Errorf("gate details did not decode: %+v", g.Details)
	}
	if g.RecordedBy != "token:tok_1" || g.RecordedAt == nil {
		t.Errorf("gate attribution did not decode: %q %+v", g.RecordedBy, g.RecordedAt)
	}
}

// TestPromotionFromWire_CarriesTheP0FieldsIntoTheDomain: mirroring the
// fields is only half the job — they have to reach release.Promotion, or a
// verb reading the domain type still sees nothing.
func TestPromotionFromWire_CarriesTheP0FieldsIntoTheDomain(t *testing.T) {
	t.Parallel()
	s := &hostedStore{}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, err := s.promotionFromWire("prod", wirePromotion{
		ID: "pr_1", EnvironmentID: "env_prod", ReleaseVersion: "v1.4.0",
		Kind: wireKindPromote, CreatedAt: at,
		FromEnvironmentID: "env_staging", FromEnvironmentName: "staging",
		FromPromotionID: "pr_source", SupersededInFlight: true,
		ResolvedArtifacts: map[string]string{"api": "sha256:" + "a1b2c3d4e5f6" + "0000000000000000000000000000000000000000000000000000"},
		Run:               &wireRun{ID: "github:acme/app/1/1", Provider: "github"},
		Gates:             []wireGate{{Name: "lint", Status: "passed"}},
		RecordedGates:     []wireGate{{Name: "smoke", Status: "failed", Summary: "1 failed", RecordedBy: "token:t1"}},
	})
	if err != nil {
		t.Fatalf("promotionFromWire: %v", err)
	}

	// The NAME, not the opaque id: forge speaks names, and P0 added the
	// name beside the id precisely so a reader need not resolve it.
	if p.FromEnv != "staging" {
		t.Errorf("FromEnv = %q, want the name %q", p.FromEnv, "staging")
	}
	if p.FromPromotionID != "pr_source" {
		t.Errorf("FromPromotionID = %q", p.FromPromotionID)
	}
	if !p.SupersededInFlight {
		t.Error("SupersededInFlight did not reach the domain type")
	}
	if p.Run.ID != "github:acme/app/1/1" {
		t.Errorf("Run = %+v", p.Run)
	}
	// The two halves of the evidence trail stay SEPARATE. Merging them
	// would lose "was this known before the button was pressed?".
	if len(p.Gates) != 1 || p.Gates[0].Name != "lint" {
		t.Errorf("pre-promote gates = %+v", p.Gates)
	}
	if len(p.RecordedGates) != 1 || p.RecordedGates[0].Name != "smoke" {
		t.Errorf("recorded gates = %+v", p.RecordedGates)
	}
	if p.RecordedGates[0].RecordedBy != "token:t1" {
		t.Errorf("recorded-by attribution lost: %+v", p.RecordedGates[0])
	}
	if got := len(p.AllGates()); got != 2 {
		t.Errorf("AllGates() = %d, want both halves", got)
	}
}

// TestPromotionFromWire_FallsBackToTheIDWithoutAName: a control plane that
// predates fromEnvironmentName sends only the id, and dropping it would
// lose the promotion path entirely.
func TestPromotionFromWire_FallsBackToTheIDWithoutAName(t *testing.T) {
	t.Parallel()
	s := &hostedStore{}
	p, err := s.promotionFromWire("prod", wirePromotion{
		ID: "pr_1", ReleaseVersion: "v1", Kind: wireKindPromote,
		FromEnvironmentID: "env_staging",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.FromEnv != "env_staging" {
		t.Errorf("FromEnv = %q, want the id as a fallback", p.FromEnv)
	}
}

// TestGateFromWire_IsLenientAndGateToWireIsStrict pins R1's two-path rule at
// the wire boundary. The ledger is append-only, so a promotion recorded
// before the status set closed can never be migrated — it MUST still read.
// Writing one back, however, must be refused.
func TestGateFromWire_IsLenientAndGateToWireIsStrict(t *testing.T) {
	t.Parallel()
	// READ: free text from an old scaffold maps to error, kept verbatim.
	got := gateFromWire(wireGate{Name: "test", Status: "all good"})
	if got.Status != release.GateStatusError {
		t.Errorf("an unrecognised stored status should read as error; got %q", got.Status)
	}
	if got.RawStatus != "all good" {
		t.Errorf("the raw status must be kept verbatim; got %q", got.RawStatus)
	}

	// WRITE: that same gate cannot be sent back.
	if _, err := gateToWire(got); err == nil {
		t.Error("a gate carrying an unrecognised raw status must not be writable")
	}
	// A status outside the set is refused outright.
	if _, err := gateToWire(release.Gate{Name: "lint", Status: release.GateStatus("green")}); err == nil {
		t.Error("a status outside the closed set must be refused on write")
	}
	// A nameless gate cannot be attributed to a check.
	if _, err := gateToWire(release.Gate{Status: release.GateStatusPassed}); err == nil {
		t.Error("a gate with no name must be refused")
	}

	// A valid gate writes every field the server accepts — and NOT the
	// server-set attribution.
	started := time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	recorded := finished.Add(time.Second)
	wire, err := gateToWire(release.Gate{
		Name: "smoke", Status: release.GateStatusPassed, URL: "https://ci/1",
		Summary: "ok", StartedAt: &started, FinishedAt: &finished,
		RunID: "github:acme/app/1/1", Details: map[string]any{"checked": 4},
		RecordedBy: "usr_should_be_dropped", RecordedAt: &recorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Summary != "ok" || wire.RunID != "github:acme/app/1/1" || wire.Details["checked"] != 4 {
		t.Errorf("gate wire = %+v", wire)
	}
	if wire.StartedAt == nil || wire.FinishedAt == nil {
		t.Error("the check's own window must be sent")
	}
	// THE ATTRIBUTION IS THE SERVER'S. A client-supplied recordedBy
	// would be a client claiming who vouched for a check — the one part
	// of a piece of evidence that must not come from the party being
	// vouched for.
	if wire.RecordedBy != "" || wire.RecordedAt != nil {
		t.Errorf("recordedBy/recordedAt must never be sent; got %q %+v", wire.RecordedBy, wire.RecordedAt)
	}
}

// TestWireRollout_DecodesEveryP0Field covers the §3.2 wait document: the
// phase, the per-workload replica counts H1 added, and the unpinned list.
func TestWireRollout_DecodesEveryP0Field(t *testing.T) {
	t.Parallel()
	const doc = `{
      "promotion": {"id": "pr_1", "releaseVersion": "v1.4.0", "kind": "DEPLOY_PROMOTION_KIND_PROMOTE"},
      "phase": "DEPLOY_ROLLOUT_PHASE_DEGRADED",
      "startedAt": "2026-10-01T00:00:00Z",
      "finishedAt": "2026-10-01T00:05:00Z",
      "stabilityWindowMs": "120000",
      "convergesPromotions": true,
      "reason": "api: not serving on sha256:ab12",
      "workloads": [{
        "deploymentId": "dep_1", "name": "api", "artifact": "api",
        "pinnedDigest": "sha256:ab12", "desiredDigest": "sha256:ab12",
        "observedDigest": "sha256:0000",
        "observedState": "DEPLOY_OBSERVED_STATE_DEGRADED",
        "verdict": "DEPLOY_VERDICT_DEGRADED",
        "phase": "DEPLOY_ROLLOUT_PHASE_DEGRADED",
        "stableSince": "2026-10-01T00:03:00Z",
        "convergedAt": "2026-10-01T00:04:00Z",
        "lastError": "CrashLoopBackOff",
        "updatedReplicas": 0, "desiredReplicas": 3
      }],
      "unpinned": [{"deploymentId": "dep_2", "name": "db", "observedState": "DEPLOY_OBSERVED_STATE_READY"}]
    }`
	var w wireRollout
	if err := json.Unmarshal([]byte(doc), &w); err != nil {
		t.Fatal(err)
	}
	if w.Phase != wireRolloutPhaseDegraded {
		t.Errorf("phase = %q", w.Phase)
	}
	if w.StabilityWindowMS != 120000 || !w.ConvergesPromotions {
		t.Errorf("window/converges = %d %v", w.StabilityWindowMS, w.ConvergesPromotions)
	}
	if w.Reason == "" || w.StartedAt == nil || w.FinishedAt == nil {
		t.Errorf("rollout envelope = %+v", w)
	}
	if w.Promotion.ID != "pr_1" {
		t.Errorf("the rollout's promotion did not decode: %+v", w.Promotion)
	}
	if len(w.Workloads) != 1 || len(w.Unpinned) != 1 {
		t.Fatalf("workloads=%d unpinned=%d", len(w.Workloads), len(w.Unpinned))
	}

	wl := w.Workloads[0]
	// THE REPLICA COUNTS ARE THE POINT of H1. Under RollingUpdate old
	// ready pods keep readyReplicas up while the new ReplicaSet
	// crash-loops, so a rollout with updatedReplicas 0 of 3 has never
	// served a request — and a gate that could not see these counts
	// would report it healthy.
	if wl.UpdatedReplicas != 0 || wl.DesiredReplicas != 3 {
		t.Errorf("replica counts = %d/%d, want 0/3", wl.UpdatedReplicas, wl.DesiredReplicas)
	}
	if wl.Artifact != "api" || wl.PinnedDigest != "sha256:ab12" || wl.ObservedDigest != "sha256:0000" {
		t.Errorf("workload digests = %+v", wl)
	}
	if wl.LastError != "CrashLoopBackOff" || wl.StableSince == nil || wl.ConvergedAt == nil {
		t.Errorf("workload detail = %+v", wl)
	}
}

// TestRolloutPhaseName_RendersAndNeverGuesses: an unrecognised phase is
// returned verbatim, because a phase forge does not understand must not be
// displayed as a success or a failure.
func TestRolloutPhaseName_RendersAndNeverGuesses(t *testing.T) {
	t.Parallel()
	for wire, want := range map[string]string{
		wireRolloutPhaseSucceeded:           "succeeded",
		wireRolloutPhaseDegraded:            "degraded",
		wireRolloutPhaseStabilizing:         "stabilizing",
		wireRolloutPhaseSuperseded:          "superseded",
		wireRolloutPhaseUnknown:             "unknown",
		wireRolloutPhaseUnspecified:         "unspecified",
		"":                                  "unspecified",
		"DEPLOY_ROLLOUT_PHASE_FUTURE_THING": "future_thing",
		"SOMETHING_ELSE":                    "SOMETHING_ELSE",
	} {
		if got := rolloutPhaseName(wire); got != want {
			t.Errorf("rolloutPhaseName(%q) = %q, want %q", wire, got, want)
		}
	}
}

// TestExitCodeForRolloutPhase_IsTheSharedTable pins §3.2's exit codes. The
// three subtle rows are the reason this table exists: still-progressing at
// the deadline is 5 (retry the WAIT) and not 1 (the release is bad);
// superseded is its own 6; and unknown is 2, never folded into either,
// because "we cannot see it" is not permission.
func TestExitCodeForRolloutPhase_IsTheSharedTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		phase string
		want  int
	}{
		{wireRolloutPhaseSucceeded, exitOK},
		{wireRolloutPhaseDegraded, exitWrong},
		{wireRolloutPhasePending, exitTimedOut},
		{wireRolloutPhaseProgressing, exitTimedOut},
		{wireRolloutPhaseStabilizing, exitTimedOut},
		{wireRolloutPhaseSuperseded, exitSuperseded},
		{wireRolloutPhaseUnknown, exitUndetermined},
		// A phase forge does not recognise is unobservable TO FORGE,
		// which is what 2 means. Reading it as a pass would be the
		// dangerous default.
		{"DEPLOY_ROLLOUT_PHASE_FUTURE_THING", exitUndetermined},
		{"", exitUndetermined},
	} {
		if got := exitCodeForRolloutPhase(tc.phase); got != tc.want {
			t.Errorf("exitCodeForRolloutPhase(%q) = %d, want %d", tc.phase, got, tc.want)
		}
	}
}

// TestWireRelease_CarriesTheRun — DeployRelease.run (§4.4 tag 9) is the join
// key that ties a release to the promotions and gates from the same run.
func TestWireRelease_CarriesTheRun(t *testing.T) {
	t.Parallel()
	const doc = `{
      "id": "rel_1", "version": "v1.4.0", "createdAt": "2026-10-01T00:00:00Z",
      "artifacts": [{"name": "api", "kind": "oci", "mode": "shared", "variant": "*",
                     "digest": "sha256:c0ffee000000000000000000000000000000000000000000000000000000beef"}],
      "run": {"id": "github:acme/app/12345/1", "url": "https://gh/run/12345", "provider": "github"}
    }`
	var w wireRelease
	if err := json.Unmarshal([]byte(doc), &w); err != nil {
		t.Fatal(err)
	}
	if w.Run == nil || w.Run.ID != "github:acme/app/12345/1" {
		t.Fatalf("release run did not decode: %+v", w.Run)
	}
	r, err := releaseFromWire(w)
	if err != nil {
		t.Fatalf("releaseFromWire: %v", err)
	}
	if r.Run.ID != "github:acme/app/12345/1" || r.Run.Provider != "github" {
		t.Errorf("the run must reach the domain type: %+v", r.Run)
	}
}

// TestWirePromoteRefusal_DecodesWhatIsThere: the refusal names the promotion
// that actually landed, which is what lets a pipeline print "prod is on
// v1.9.1" instead of "someone else promoted" and sending a human to a
// dashboard.
func TestWirePromoteRefusal_DecodesWhatIsThere(t *testing.T) {
	t.Parallel()
	const doc = `{
      "reason": "promotion_conflict",
      "expectedCurrentPromotionId": "pr_expected",
      "expectedUnbound": false,
      "actualPhase": "DEPLOY_ROLLOUT_PHASE_PROGRESSING",
      "detail": "prod is on v1.9.1, promoted by alice",
      "actualCurrent": {
        "id": "pr_actual", "releaseVersion": "v1.9.1",
        "kind": "DEPLOY_PROMOTION_KIND_PROMOTE", "promotedByUserId": "usr_alice"
      }
    }`
	var w wirePromoteRefusal
	if err := json.Unmarshal([]byte(doc), &w); err != nil {
		t.Fatal(err)
	}
	if w.Reason != reasonPromotionConflict {
		t.Errorf("reason = %q", w.Reason)
	}
	if w.ExpectedCurrentPromotionID != "pr_expected" {
		t.Errorf("the expectation must be echoed so a log line is self-contained; got %q",
			w.ExpectedCurrentPromotionID)
	}
	if w.ActualCurrent == nil || w.ActualCurrent.ReleaseVersion != "v1.9.1" {
		t.Fatalf("actualCurrent did not decode: %+v", w.ActualCurrent)
	}
	if w.ActualPhase != wireRolloutPhaseProgressing {
		t.Errorf("actualPhase = %q", w.ActualPhase)
	}
	// And the reason maps to the exit code a pipeline branches on.
	if got := exitCodeForRefusal(w.Reason); got != exitConflict {
		t.Errorf("exit code = %d, want %d", got, exitConflict)
	}
}

// TestWireRunStage_Decodes — §3.7's derived timeline entry.
func TestWireRunStage_Decodes(t *testing.T) {
	t.Parallel()
	const doc = `{
      "kind": "promote", "name": "prod", "environmentId": "env_prod",
      "promotionId": "pr_1", "status": "passed",
      "startedAt": "2026-10-01T00:00:00Z", "finishedAt": "2026-10-01T00:01:00Z",
      "url": "https://gh/run/1", "summary": "v1.4.0 → prod"
    }`
	var w wireRunStage
	if err := json.Unmarshal([]byte(doc), &w); err != nil {
		t.Fatal(err)
	}
	if w.Kind != "promote" || w.PromotionID != "pr_1" || w.Status != "passed" {
		t.Errorf("stage = %+v", w)
	}
	if w.StartedAt == nil || w.FinishedAt == nil || w.Summary == "" {
		t.Errorf("stage detail = %+v", w)
	}
	// The kind and status are R1's closed sets, so they must parse.
	if _, err := release.ParseStageKind(w.Kind); err != nil {
		t.Errorf("stage kind should be in R1's closed set: %v", err)
	}
	if _, err := release.ParseStageStatus(w.Status); err != nil {
		t.Errorf("stage status should be in R1's closed set: %v", err)
	}
}
