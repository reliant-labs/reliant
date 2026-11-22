package cli

// The three rules that are not about field names: the client-side plan-digest
// check, the new refusal reasons, and F-15.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// buildTestPlan builds a real plan through release.BuildPlan, so its digest is
// one the server would compute rather than a literal somebody typed.
func buildTestPlan(t *testing.T) release.Plan {
	t.Helper()
	live := dsotTestShape()
	cand := dsotTestShape()
	// A stateful deletion: a stop-class finding, which is what the
	// acknowledgement rules are about.
	live.Objects = append(live.Objects, release.ShapeObject{
		Cluster: "prod-ctx", Kind: "PersistentVolumeClaim", Namespace: "app",
		Name: "data", Hash: "sha256:" + rep64('9'),
	})
	p, err := release.BuildPlan(release.PlanInput{
		EnvironmentID: "env_prod", BundleID: "bnd_1", ReleaseVersion: "v1.4.0",
		Candidate: cand, CandidateConfigDigest: "sha256:" + rep64('e'),
		Live:  &live,
		Basis: release.PlanBasis{CurrentPromotionID: "pr_0", AppliedBundleID: "bnd_0"},
		Drift: &release.DriftObservation{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.StopCodes()) == 0 {
		t.Fatal("the fixture plan must hold a stop-class finding")
	}
	return p
}

// planToWireFixture renders a plan as the control plane's protojson would.
// Used by the request tests to answer PlanDeploy with a plan whose digest is
// genuinely self-consistent.
func planToWireFixture(p release.Plan) map[string]any {
	findings := make([]any, 0, len(p.Findings))
	for _, f := range p.Findings {
		row := map[string]any{"code": f.Code, "class": string(f.Class)}
		if f.Section != "" {
			row["section"] = f.Section
		}
		if f.Subject != "" {
			row["subject"] = f.Subject
		}
		if f.Detail != "" {
			row["detail"] = f.Detail
		}
		findings = append(findings, row)
	}
	basis := map[string]any{}
	if p.LiveBasis.CurrentPromotionID != "" {
		basis["currentPromotionId"] = p.LiveBasis.CurrentPromotionID
	}
	if p.LiveBasis.AppliedBundleID != "" {
		basis["appliedBundleId"] = p.LiveBasis.AppliedBundleID
	}
	if p.LiveBasis.AppliedConfigDigest != "" {
		basis["appliedConfigDigest"] = p.LiveBasis.AppliedConfigDigest
	}
	if p.LiveBasis.DriftObserved {
		basis["driftObserved"] = true
	}
	out := map[string]any{
		"digest":        p.Digest,
		"environmentId": p.EnvironmentID,
		"bundleId":      p.BundleID,
		"liveBasis":     basis,
		"findings":      findings,
		"computedAt":    "2026-10-02T10:00:30Z",
	}
	if p.ReleaseVersion != "" {
		out["releaseVersion"] = p.ReleaseVersion
	}
	if p.ConfigIdentical {
		out["configIdentical"] = true
	}
	return out
}

// TestPlanDeploy_VerifiesTheDigestAndRefusesAMismatch is the client-side half
// of O-13's guarantee.
//
// It is NOT defence against a hostile server — one that wanted to lie would
// send a self-consistent lie. It is the check that both sides run the same
// release.BuildPlan: the digest covers the plan's meaning, so a disagreement
// means a version skew. Catching it here, as a named refusal, is the
// difference between discovering that now and discovering it later as a
// `plan_stale` refusal at approval time — which reads as "someone promoted
// under me" and sends an operator to the wrong investigation.
func TestPlanDeploy_VerifiesTheDigestAndRefusesAMismatch(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)

	// The honest plan verifies.
	good := &fakeDSOTCaller{replies: map[string]any{
		procPlanDeploy: map[string]any{"plan": planToWireFixture(plan)},
	}}
	got, err := hostedPlanClient{client: good}.PlanDeploy(context.Background(), "env_prod", "bnd_1", "v1.4.0")
	if err != nil {
		t.Fatalf("a self-consistent plan must be accepted: %v", err)
	}
	if ok, verr := got.VerifyDigest(); verr != nil || !ok {
		t.Errorf("the decoded plan must re-verify: ok=%v err=%v", ok, verr)
	}
	if len(got.StopCodes()) == 0 {
		t.Error("the stop-class findings must survive the decode; they are what needs acknowledging")
	}

	// A digest that does not describe the content. Spelled as "the server
	// sent a plan whose findings were computed differently" — which is
	// exactly what a version skew looks like: same digest, one more
	// finding.
	drifted := planToWireFixture(plan)
	drifted["findings"] = append(drifted["findings"].([]any), map[string]any{
		"code": release.FindingObjectAdded, "class": string(release.ClassInfo),
		"section": release.SectionObjects, "subject": "prod-ctx/Service/app/api",
	})
	bad := &fakeDSOTCaller{replies: map[string]any{
		procPlanDeploy: map[string]any{"plan": drifted},
	}}
	_, err = hostedPlanClient{client: bad}.PlanDeploy(context.Background(), "env_prod", "bnd_1", "v1.4.0")
	if err == nil {
		t.Fatal("a plan whose digest does not recompute must be REFUSED, not approved")
	}
	if !errors.Is(err, errPlanDigestMismatch) {
		t.Errorf("want errPlanDigestMismatch, got %v", err)
	}
	if !errors.Is(err, release.ErrInvalid) {
		t.Errorf("the refusal is an invalid-input failure; got %v", err)
	}
}

// TestPlanFromWire_RefusesAnUnreadableFinding: a finding whose class forge
// cannot read must not default to `info`, which is the class a blanket
// approval covers. The unreadable one might be the stop-class finding.
func TestPlanFromWire_RefusesAnUnreadableFinding(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(map[string]any){
		"unknown class": func(f map[string]any) { f["class"] = "probably_fine" },
		"unknown code":  func(f map[string]any) { f["code"] = "some_future_finding" },
	} {
		t.Run(name, func(t *testing.T) {
			w := wirePlan{
				Digest: "sha256:" + rep64('0'), EnvironmentID: "env_prod", BundleID: "bnd_1",
				Findings: []wirePlanFinding{{Code: release.FindingObjectAdded, Class: string(release.ClassInfo), Section: release.SectionObjects}},
			}
			raw := map[string]any{"code": w.Findings[0].Code, "class": w.Findings[0].Class}
			mutate(raw)
			w.Findings[0] = wirePlanFinding{Code: raw["code"].(string), Class: raw["class"].(string), Section: release.SectionObjects}
			if _, err := planFromWire(w); err == nil {
				t.Fatal("want a refusal; a finding forge cannot read must never decode as info")
			} else if !errors.Is(err, release.ErrInvalid) {
				t.Errorf("want release.ErrInvalid, got %v", err)
			}
		})
	}
}

// TestPlanFromWire_RefusesAPlanWithNoDigest: the digest is what an approval
// NAMES, so a plan without one cannot be approved at all.
func TestPlanFromWire_RefusesAPlanWithNoDigest(t *testing.T) {
	t.Parallel()
	if _, err := planFromWire(wirePlan{EnvironmentID: "env_prod", BundleID: "bnd_1"}); err == nil {
		t.Fatal("want a refusal for a plan with no digest")
	}
}

// ─── Refusal reasons ─────────────────────────────────────────────────────────

// TestExitCodeForRefusal_O13Reasons pins the three codes the doc names.
//
// plan_stale shares exitConflict (3) with promotion_conflict DELIBERATELY:
// to the operator, "someone promoted under me" and "the world changed under
// me" are one situation with one response. plan_unacknowledged is exitRefused
// (4) because nothing moved and nothing was lost — re-running with the
// finding acknowledged is the legitimate next step. bundle_missing is
// exitWrong (1): we looked, and the bytes are not there, and no amount of
// waiting makes a deleted blob reappear.
func TestExitCodeForRefusal_O13Reasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		want   int
	}{
		{reasonPlanStale, exitConflict},
		{reasonPlanUnacknowledged, exitRefused},
		{reasonBundleMissing, exitWrong},
		// Unchanged, so the new rows cannot have moved an old one.
		{reasonPromotionConflict, exitConflict},
		{reasonRolloutInFlight, exitRefused},
		// L4 (#516) reasons forge does not know yet. exitWrong, never
		// exitUndetermined: the server refused, which means it looked.
		{"source_policy", exitWrong},
		{"apply_in_flight", exitWrong},
	} {
		if got := exitCodeForRefusal(tc.reason); got != tc.want {
			t.Errorf("exitCodeForRefusal(%q) = %d, want %d", tc.reason, got, tc.want)
		}
		if tc.reason != "source_policy" && tc.reason != "apply_in_flight" {
			if refusalHint(tc.reason) == "" {
				t.Errorf("reason %q has no next step for the operator", tc.reason)
			}
		}
	}
}

// TestPromoteRefusal_PlanStaleCarriesTheRecomputedPlan: the refusal carries
// the plan the server JUDGED AGAINST, so a refused caller shows the operator
// what changed without a second round trip — and without a second PlanDeploy,
// which would be computed at yet another moment and could differ from the one
// that actually refused the write.
func TestPromoteRefusal_PlanStaleCarriesTheRecomputedPlan(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)
	detail, err := json.Marshal(map[string]any{
		"reason":                     reasonPlanStale,
		"expectedCurrentPromotionId": "pr_0",
		"detail":                     "the environment moved after the plan was computed",
		"currentPlan":                planToWireFixture(plan),
	})
	if err != nil {
		t.Fatal(err)
	}
	wireErr := &cloud.Error{
		Code: cloud.CodeFailedPrecondition, Reason: reasonPlanStale,
		Message: "refused",
		Details: []cloud.ErrorDetail{{Type: promoteRefusalType, Debug: detail}},
	}

	refused := (&hostedStore{}).refusalFromWire("prod", wireErr)
	if refused == nil {
		t.Fatal("a reason-carrying failure is a refusal")
	}
	if refused.Reason != reasonPlanStale {
		t.Errorf("reason = %q", refused.Reason)
	}
	if refused.ExitCode() != exitConflict {
		t.Errorf("exit = %d, want %d (same as promotion_conflict, on purpose)", refused.ExitCode(), exitConflict)
	}
	if refused.CurrentPlan == nil {
		t.Fatal("plan_stale must carry the recomputed plan; without it the operator cannot see what changed")
	}
	if refused.CurrentPlan.Digest != plan.Digest {
		t.Errorf("carried plan digest = %q, want %q", refused.CurrentPlan.Digest, plan.Digest)
	}
	// The --json object carries the same evidence a human reading the
	// message gets.
	if got := refused.toJSON(); got.CurrentPlan == nil || got.CurrentPlan.Digest != plan.Digest {
		t.Error("--json must carry the recomputed plan too")
	}
}

// TestPromoteRefusal_PlanUnacknowledgedNamesTheCodes: the operator needs the
// stop-class codes to acknowledge, and they are DERIVED from the carried plan
// so the list printed and the plan shown cannot disagree.
func TestPromoteRefusal_PlanUnacknowledgedNamesTheCodes(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)
	detail, _ := json.Marshal(map[string]any{
		"reason":      reasonPlanUnacknowledged,
		"detail":      "1 stop-class finding was not acknowledged",
		"currentPlan": planToWireFixture(plan),
	})
	refused := (&hostedStore{}).refusalFromWire("prod", &cloud.Error{
		Code: cloud.CodeFailedPrecondition, Reason: reasonPlanUnacknowledged,
		Details: []cloud.ErrorDetail{{Type: promoteRefusalType, Debug: detail}},
	})
	if refused == nil {
		t.Fatal("want a refusal")
	}
	if refused.ExitCode() != exitRefused {
		t.Errorf("exit = %d, want %d (nothing was lost; acknowledging and re-running is legitimate)", refused.ExitCode(), exitRefused)
	}
	want := plan.StopCodes()
	if len(refused.Unacknowledged) != len(want) || len(want) == 0 {
		t.Fatalf("unacknowledged = %v, want %v", refused.Unacknowledged, want)
	}
	for i := range want {
		if refused.Unacknowledged[i] != want[i] {
			t.Errorf("unacknowledged[%d] = %q, want %q", i, refused.Unacknowledged[i], want[i])
		}
	}
	if !contains(refused.Error(), "--acknowledge-destructive") {
		t.Errorf("the message must name the flag; got %q", refused.Error())
	}
}

// TestPromoteRefusal_AnUnreadablePlanDoesNotHideTheRefusal: the reason already
// decided the exit code, and the plan only enriches the message. Turning a
// clear refusal into a decode error would hide WHY the write was declined
// behind a complaint about the explanation.
func TestPromoteRefusal_AnUnreadablePlanDoesNotHideTheRefusal(t *testing.T) {
	t.Parallel()
	detail, _ := json.Marshal(map[string]any{
		"reason": reasonPlanStale,
		"detail": "stale",
		// A digest that does not describe the content, so planFromWire
		// refuses it.
		"currentPlan": map[string]any{
			"digest": "sha256:" + rep64('0'), "environmentId": "env_prod", "bundleId": "bnd_1",
		},
	})
	refused := (&hostedStore{}).refusalFromWire("prod", &cloud.Error{
		Code: cloud.CodeFailedPrecondition, Reason: reasonPlanStale,
		Details: []cloud.ErrorDetail{{Type: promoteRefusalType, Debug: detail}},
	})
	if refused == nil {
		t.Fatal("the refusal must stand even when its plan will not decode")
	}
	if refused.Reason != reasonPlanStale || refused.ExitCode() != exitConflict {
		t.Errorf("refusal = %+v", refused)
	}
	if refused.CurrentPlan != nil {
		t.Error("an undecodable plan is DROPPED, not half-adopted")
	}
	if refused.Detail != "stale" {
		t.Errorf("the server's detail must survive; got %q", refused.Detail)
	}
}

// ─── F-15 ────────────────────────────────────────────────────────────────────

// TestF15_UnimplementedIsATypedError: a server older than this forge answers
// UNIMPLEMENTED on RecordBundle. forge types that rather than
// deciding what to do about it — F6a chooses the fallback, and once protected
// envs exist (#516, L6) a protected env refuses instead. Baking either
// behaviour in here would put the weaker of the two in the wire layer, where
// neither caller could override it.
//
// Recognised by the CONNECT CODE, never message text: an old server's prose
// is not a contract.
func TestF15_UnimplementedIsATypedError(t *testing.T) {
	t.Parallel()
	unimplemented := &cloud.Error{
		Code:    cloud.CodeUnimplemented,
		Message: "controlplane.v1.DeployService/RecordBundle is not implemented",
	}

	t.Run("RecordBundle", func(t *testing.T) {
		f := &fakeDSOTCaller{errs: map[string]error{procRecordBundle: unimplemented}}
		_, _, err := hostedBundleClient{client: f}.RecordBundle(context.Background(), "prod", "env_prod",
			"r", []byte("{}"), []byte("{}"), release.Run{})
		assertPredatesBundles(t, err)
	})
}

func assertPredatesBundles(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	if !errors.Is(err, errControlPlanePredatesBundles) {
		t.Fatalf("want errControlPlanePredatesBundles, got %T: %v", err, err)
	}
	if !contains(err.Error(), "this control plane predates bundles; upgrade it") {
		t.Errorf("the message is the doc's; got %q", err.Error())
	}
	// The wire failure is still reachable, so a caller that wants the
	// detail has it.
	var cerr *cloud.Error
	if !errors.As(err, &cerr) || !cerr.HasCode(cloud.CodeUnimplemented) {
		t.Error("the underlying cloud.Error must stay unwrappable")
	}
}

// TestF15_IsNotTriggeredByOtherFailures: an Unavailable control plane, or a
// refusal, must NOT read as "the server predates bundles". Mapping any
// failure to that would make forge fall back to the pre-bundle path during a
// transient outage — recording nothing, which is the one thing this design
// exists to prevent.
func TestF15_IsNotTriggeredByOtherFailures(t *testing.T) {
	t.Parallel()
	for name, wireErr := range map[string]error{
		"unavailable":    &cloud.Error{Code: cloud.CodeUnavailable, Message: "down"},
		"permission":     &cloud.Error{Code: cloud.CodePermissionDenied, Message: "no"},
		"not a cp error": errors.New("unimplemented"),
		"message only":   &cloud.Error{Code: cloud.CodeInvalidArgument, Message: "unimplemented, sorry"},
		"failed_precond": &cloud.Error{Code: cloud.CodeFailedPrecondition, Message: "refused"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDSOTCaller{errs: map[string]error{procRecordBundle: wireErr}}
			_, _, err := hostedBundleClient{client: f}.RecordBundle(context.Background(), "prod", "e",
				"r", []byte("{}"), []byte("{}"), release.Run{})
			if errors.Is(err, errControlPlanePredatesBundles) {
				t.Errorf("%v must not be read as an old control plane", wireErr)
			}
		})
	}
}

// ─── Promote's new wire fields ───────────────────────────────────────────────

// TestPromote_SendsPlanDigestAndAcknowledgements pins PromoteReleaseRequest
// tags 13–14, and that forge never sends `approvedBy`.
func TestPromote_SendsPlanDigestAndAcknowledgements(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		"controlplane.v1.DeployService/EnsureEnvironment": map[string]any{
			"environment": map[string]any{"id": "env_prod", "name": "prod"}, "created": false,
		},
		procPromote: map[string]any{"promotion": map[string]any{
			"id": "pr_1", "environmentId": "env_prod", "releaseId": "rel_1",
			"releaseVersion": "v1.4.0", "kind": wireKindPromote,
			"createdAt":  "2026-10-02T10:00:00Z",
			"planDigest": "sha256:" + rep64('f'), "approvedBy": "usr_1",
			"acknowledgedFindings": []any{release.FindingStatefulDeletion},
		}},
	}}
	store := &hostedStore{client: f, resolver: cloudEnvResolver{client: f, project: "hounders"}, project: "hounders", kind: "DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED"}
	got, err := store.Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v1.4.0", Kind: release.KindPromote,
		Resolved:             map[string]string{"api": "sha256:" + rep64('c')},
		PlanDigest:           "sha256:" + rep64('f'),
		AcknowledgedFindings: []string{release.FindingStatefulDeletion},
		// A client-supplied approver, which must be dropped.
		ApprovedBy: "whoever-I-say",
	}, appendGuard{ExpectedCurrentID: "pr_0"})
	if err != nil {
		t.Fatal(err)
	}

	body := f.body(t, procPromote)
	wantFields(t, body, map[string]any{
		"environmentId":              "env_prod",
		"version":                    "v1.4.0",
		"expectedCurrentPromotionId": "pr_0",
		"planDigest":                 "sha256:" + rep64('f'),
		"acknowledgedFindings":       []any{release.FindingStatefulDeletion},
	})
	if _, present := body["approvedBy"]; present {
		t.Error("approvedBy is SERVER-SET from the credential; an approval whose approver the approver chose is not an approval")
	}

	// And the read-back carries the server's record of the review.
	if got.PlanDigest != "sha256:"+rep64('f') {
		t.Errorf("read-back planDigest = %q", got.PlanDigest)
	}
	if got.ApprovedBy != "usr_1" {
		t.Errorf("read-back approvedBy = %q — the server's answer, not what forge sent", got.ApprovedBy)
	}
	if len(got.AcknowledgedFindings) != 1 {
		t.Errorf("read-back acknowledgedFindings = %v", got.AcknowledgedFindings)
	}
}

// TestPromote_OmitsThePlanWhenThereIsNone: a control plane that predates O-13
// discards unknown fields, so forge may send these before the server deploys
// — but a promote with no plan must send no plan rather than an empty digest,
// which the server would have to tell apart from a real one.
func TestPromote_OmitsThePlanWhenThereIsNone(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		"controlplane.v1.DeployService/EnsureEnvironment": map[string]any{
			"environment": map[string]any{"id": "env_prod", "name": "prod"},
		},
		procPromote: map[string]any{"promotion": map[string]any{
			"id": "pr_1", "environmentId": "env_prod", "releaseId": "rel_1",
			"releaseVersion": "v1.4.0", "kind": wireKindPromote,
			"createdAt": "2026-10-02T10:00:00Z",
		}},
	}}
	store := &hostedStore{client: f, resolver: cloudEnvResolver{client: f, project: "hounders"}, project: "hounders", kind: "DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED"}
	if _, err := store.Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v1.4.0", Kind: release.KindPromote,
		Resolved: map[string]string{},
	}, appendGuard{ExpectUnbound: true}); err != nil {
		t.Fatal(err)
	}
	body := f.body(t, procPromote)
	for _, absent := range []string{"planDigest", "acknowledgedFindings"} {
		if _, present := body[absent]; present {
			t.Errorf("a promote with no plan must not send %q", absent)
		}
	}
}

// TestLiveEnvironment_RefusesAnUnrecognisedKind: the kind decides whether an
// env's secrets are pullable and whether it is a deploy target, so a value
// forge cannot read is refused rather than guessed.
func TestLiveEnvironment_RefusesAnUnrecognisedKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"", "DEPLOY_ENVIRONMENT_KIND_UNSPECIFIED", "DEPLOY_ENVIRONMENT_KIND_SOMETHING_NEW"} {
		_, err := liveEnvFromWire(wireLiveEnvRow{
			Environment: &wireLiveEnvironment{ID: "env_x", Name: "x", Kind: kind},
		}, time.Now())
		if err == nil {
			t.Errorf("kind %q must be refused, not defaulted", kind)
		}
	}
	// And all four real kinds are recognised, PREVIEW included — forge
	// never creates one, but it must be able to read one.
	for _, kind := range []deployEnvKind{
		deployEnvKindPersistent, deployEnvKindPreview, deployEnvKindSelfManaged, deployEnvKindLocal,
	} {
		if _, err := envKindFromWire(string(kind)); err != nil {
			t.Errorf("kind %q must decode: %v", kind, err)
		}
	}
}
