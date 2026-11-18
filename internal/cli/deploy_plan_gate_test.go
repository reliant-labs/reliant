package cli

// The server-binding half of the approval gate (O-13, doc §8.6, F-19).
//
// WHAT MAKES THESE WORTH WRITING. Every failure here is a deploy that
// PROCEEDS when it should not, and none of them produces an error anywhere
// else: an --approve that is not checked silently becomes a --yes, and a
// stop-class finding that --yes covers silently becomes a pre-approval of
// every future destructive change to that env. The symptom is a successful
// deploy, which is why the checks need tests that assert on the refusal rather
// than on the happy path.

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// gateTestPlan is a plan with one stop-class finding and one info finding,
// built through release.BuildPlan so its digest is one a server would compute
// rather than a literal somebody typed.
func gateTestPlan(t *testing.T) release.Plan {
	t.Helper()
	return buildTestPlan(t)
}

// TestPlanGate_ApproveMismatchRefuses is --approve's whole purpose. The
// stronger form of approval is "proceed only if the plan is exactly this", and
// a plan that changed between the stages must be REFUSED rather than
// re-approved blind.
func TestPlanGate_ApproveMismatchRefuses(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)

	err := checkPlanApproval("prod", plan, deployApproval{
		Digest: "sha256:" + rep64('9'),
		// Every stop finding acknowledged, so the ONLY thing that can
		// refuse this is the digest mismatch.
		AcknowledgedFindings: plan.StopCodes(),
	}, true)
	if err == nil {
		t.Fatal("--approve naming a different plan must refuse: a plan that changed between the stages " +
			"of a pipeline must not be re-approved blind")
	}
	// Exit 3, the same code a CAS mismatch and a server plan_stale produce:
	// to the operator these are one situation with one response.
	if code := exitCodeOf(t, err); code != exitConflict {
		t.Errorf("exit code = %d, want %d (plan_stale shares exit 3 with a CAS conflict)", code, exitConflict)
	}
	msg := err.Error()
	// BOTH digests must be named. "The plan changed" without saying from
	// what to what leaves the operator running a second command.
	if !strings.Contains(msg, plan.Digest) || !strings.Contains(msg, rep64('9')) {
		t.Errorf("the refusal must name the approved digest AND the current one; got: %s", msg)
	}
}

// TestPlanGate_ApproveMatchProceeds: the gate must not refuse a correct
// approval. Without this, a gate that refused everything would pass the test
// above.
func TestPlanGate_ApproveMatchProceeds(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)

	if err := checkPlanApproval("prod", plan, deployApproval{
		Digest:               plan.Digest,
		AcknowledgedFindings: plan.StopCodes(),
	}, true); err != nil {
		t.Fatalf("an approval naming this exact plan, with its stop findings acknowledged, must proceed: %v", err)
	}
}

// TestPlanGate_StopFindingRefusesEvenWithYes is the rule that is most likely
// to be read as a bug, and is the entire point of the second flag.
//
// --yes means "I read the plan". It does NOT cover a stateful deletion or a
// load-balancer identity change, because --yes is the flag that ends up
// hard-coded in a CI workflow — and the moment it is, a --yes that covered
// destructive changes would silently pre-approve every future one against that
// env. A flag that NAMES the finding cannot be pre-approved in advance,
// because the code is not knowable until the plan is computed.
func TestPlanGate_StopFindingRefusesEvenWithYes(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)
	if len(plan.StopCodes()) == 0 {
		t.Fatal("precondition: the fixture plan must carry a stop-class finding")
	}

	err := checkPlanApproval("prod", plan, deployApproval{}, true)
	if err == nil {
		t.Fatal("--yes must NOT cover a stop-class finding: a --yes hard-coded in CI would otherwise " +
			"pre-approve every future destructive change to this env")
	}
	// Exit 4 (refused), not 3 (conflict): nothing moved and nothing was
	// lost, so re-running with the finding acknowledged is the legitimate
	// next step — which is what a pipeline must be told apart from "someone
	// moved the env under you".
	if code := exitCodeOf(t, err); code != exitRefused {
		t.Errorf("exit code = %d, want %d (nothing moved; the write was declined pending a decision)", code, exitRefused)
	}
	msg := err.Error()
	for _, code := range plan.StopCodes() {
		if !strings.Contains(msg, code) {
			t.Errorf("the refusal must name stop code %q so it can be acknowledged; got: %s", code, msg)
		}
	}
	// And it must say WHY --yes did not cover it, or the next reader files
	// this as a bug.
	if !strings.Contains(msg, "--yes") {
		t.Errorf("the refusal must explain that --yes deliberately does not cover this; got: %s", msg)
	}
	if !strings.Contains(msg, "--acknowledge-destructive") {
		t.Errorf("the refusal must name the flag that does cover it; got: %s", msg)
	}
}

// TestPlanGate_AcknowledgingEveryStopCodeProceeds: naming them all is what
// gets through.
func TestPlanGate_AcknowledgingEveryStopCodeProceeds(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)

	if err := checkPlanApproval("prod", plan, deployApproval{
		AcknowledgedFindings: plan.StopCodes(),
	}, true); err != nil {
		t.Fatalf("every stop finding acknowledged must proceed: %v", err)
	}
}

// TestPlanGate_PartialAcknowledgementRefusesTheRest: acknowledging one stop
// code does not cover another.
//
// The failure this prevents is a pipeline that learned one code once and
// carries it forever, which would then sail past a DIFFERENT destructive
// change — exactly the pre-approval the second flag exists to make impossible.
func TestPlanGate_PartialAcknowledgementRefusesTheRest(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)
	// Two stop findings of different codes.
	plan.Findings = append(plan.Findings, release.PlanFinding{
		Code:    release.FindingLBIdentityChange,
		Class:   release.ClassStop,
		Section: release.SectionLB,
		Subject: "prod-ctx/Service/app/api",
		Detail:  "the load balancer loses its external address",
	})

	err := checkPlanApproval("prod", plan, deployApproval{
		AcknowledgedFindings: []string{release.FindingStatefulDeletion},
	}, true)
	if err == nil {
		t.Fatal("acknowledging one stop code must not cover a different one")
	}
	if !strings.Contains(err.Error(), release.FindingLBIdentityChange) {
		t.Errorf("the refusal must name the code still outstanding; got: %v", err)
	}
}

// TestPlanGate_AcknowledgingAFindingThePlanNoLongerHasIsFine: a two-stage
// pipeline passes the codes from stage one's plan, and a finding that has
// since disappeared means the dangerous change is GONE.
//
// Refusing there would fail a job for having become safer, which is the kind
// of rule that teaches people to work around the gate.
func TestPlanGate_AcknowledgingAFindingThePlanNoLongerHasIsFine(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)

	acknowledged := append(plan.StopCodes(), release.FindingLBIdentityChange)
	if err := checkPlanApproval("prod", plan, deployApproval{
		Digest: plan.Digest, AcknowledgedFindings: acknowledged,
	}, true); err != nil {
		t.Fatalf("acknowledging a finding the plan no longer carries must not refuse — the dangerous "+
			"change is gone: %v", err)
	}
}

// TestPlanGate_NoPlanIsNotAnApproval: a nil plan means the findings are
// UNAVAILABLE, so --approve cannot be honoured — and must not be ignored.
//
// Silently admitting the write because forge could not compute a plan would
// turn the strongest form of approval into the weakest, invisibly. Exit 2
// (undetermined) says the approval was neither honoured nor judged, which is
// the truthful answer and the one a pipeline must not read as success.
func TestPlanGate_NoPlanIsNotAnApproval(t *testing.T) {
	t.Parallel()

	err := gateDeployOnPlan("prod", nil, deployApproval{Digest: "sha256:" + rep64('1')}, &deployConfirm{Yes: true}, false)
	if err == nil {
		t.Fatal("--approve with no plan to compare against must refuse: admitting the write would turn " +
			"the strongest approval into the weakest, silently")
	}
	if code := exitCodeOf(t, err); code != exitUndetermined {
		t.Errorf("exit code = %d, want %d — the approval was neither honoured nor judged", code, exitUndetermined)
	}
}

// TestPlanGate_NoPlanAndNoApproveProceeds: the other half. A never-built env,
// or a control plane that predates bundles, has no plan — and that is a real
// state, not a failure. The confirmation gate still stands in front of the
// write, so the deploy is not ungated.
func TestPlanGate_NoPlanAndNoApproveProceeds(t *testing.T) {
	t.Parallel()

	if err := gateDeployOnPlan("prod", nil, deployApproval{}, &deployConfirm{Yes: true}, false); err != nil {
		t.Fatalf("no plan and no --approve is a real state (never built, or F-15), not a failure: %v", err)
	}
}

// TestPlanGate_StopFindingRefusesWithNoConfirmConfigured: the destructive
// check is a property of the DEPLOY, not of whether a prompt was wired up.
//
// promoteOptions.Confirm is nil for the callers whose subject is the ledger
// itself. If the stop check rode on Confirm being non-nil, those paths would
// quietly admit destructive deploys.
func TestPlanGate_StopFindingRefusesWithNoConfirmConfigured(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)

	err := gateDeployOnPlan("prod", &plan, deployApproval{}, nil, false)
	if err == nil {
		t.Fatal("a stop-class finding must refuse even with no confirmation configured: the check is a " +
			"property of the deploy, not of whether a prompt was wired up")
	}
}

// TestRecomputedPlanIsShownOnARefusal is F-19. A stale plan comes back with
// the FRESHLY RECOMPUTED one, and the refusal has to show it — otherwise the
// operator's next act is a second command to find out what changed, at the
// exact moment they are deciding whether to re-approve.
func TestRecomputedPlanIsShownOnARefusal(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)
	refusal := &promoteRefusedError{
		Reason:      reasonPlanStale,
		Detail:      "the environment changed after the plan was computed",
		CurrentPlan: &plan,
	}

	msg := refusal.Error()
	if !strings.Contains(msg, plan.Digest) {
		t.Errorf("a plan_stale refusal must show the NEW digest, so the next approval can name it; got: %s", msg)
	}
	for _, code := range plan.StopCodes() {
		if !strings.Contains(msg, code) {
			t.Errorf("the recomputed plan's stop code %q must be shown; got: %s", code, msg)
		}
	}
	// And it is still exit 3, through the one table every verb reads.
	if refusal.ExitCode() != exitConflict {
		t.Errorf("plan_stale exit code = %d, want %d", refusal.ExitCode(), exitConflict)
	}
}

// TestRenderDeployPlan_SectionsAndDigest: the §8.6 sections are printed in the
// doc's order, and the digest is printed so a reviewer can pass it to
// --approve.
func TestRenderDeployPlan_SectionsAndDigest(t *testing.T) {
	t.Parallel()
	plan := gateTestPlan(t)
	// The fixture's diff is a single stateful deletion, so it exercises only
	// one section. Ordering needs two, and they must be stated rather than
	// assumed — a test that asserted an order over one section would pass
	// whatever the renderer did.
	plan.Findings = append(plan.Findings, release.PlanFinding{
		Code: release.FindingObjectAdded, Class: release.ClassInfo,
		Section: release.SectionObjects, Subject: "prod-ctx/ConfigMap/app/flags",
	})
	var out strings.Builder
	renderDeployPlan(&out, "prod", plan)
	got := out.String()

	if !strings.Contains(got, plan.Digest) {
		t.Errorf("the plan must print its digest — it is what --approve names; got:\n%s", got)
	}
	if !strings.Contains(got, "DESTRUCTIVE") {
		t.Errorf("a plan with a stop finding must say so prominently; got:\n%s", got)
	}
	// Section ORDER is the doc's, so an operator reads the same shape every
	// time rather than one that reorders itself run to run.
	objects := strings.Index(got, "per-object diff")
	stateful := strings.Index(got, "stateful deletions")
	if objects < 0 || stateful < 0 || objects > stateful {
		t.Errorf("sections must follow §8.6's order (per-object before stateful deletions); got:\n%s", got)
	}
}

// TestRenderDeployPlan_EmptyAndUnknownDoNotRenderAlike is F-20's rule applied
// to the renderer.
//
// "No differences" and "we could not compute this" must never look the same.
// The failure that matters is an operator reading a blank per-object section
// as "nothing changes" — which is why an unknown finding is rendered like any
// other finding and the no-findings case says so in words.
func TestRenderDeployPlan_EmptyAndUnknownDoNotRenderAlike(t *testing.T) {
	t.Parallel()

	var empty strings.Builder
	renderDeployPlan(&empty, "prod", release.Plan{
		Digest: "sha256:" + rep64('e'), Findings: []release.PlanFinding{},
	})
	var unknown strings.Builder
	renderDeployPlan(&unknown, "prod", release.Plan{
		Digest: "sha256:" + rep64('f'),
		Findings: []release.PlanFinding{{
			Code: release.FindingUnknown, Class: release.ClassWarn,
			Section: release.SectionObjects, Subject: release.SectionObjects,
			Detail: "no recorded config to compare against",
		}},
	})

	if !strings.Contains(empty.String(), "no changes found") {
		t.Errorf("an empty plan must SAY it found nothing; got:\n%s", empty.String())
	}
	if strings.Contains(unknown.String(), "no changes found") {
		t.Errorf("a plan that could not be computed must not read as 'nothing changes'; got:\n%s", unknown.String())
	}
	if !strings.Contains(unknown.String(), "warn") {
		t.Errorf("an unknown section is classed warn (F-20); got:\n%s", unknown.String())
	}
}

// TestDescribePlanBasis_NoAppliedBundleIsNamed: a plan computed against no
// applied bundle is legitimate (a first deploy), and a reader must be able to
// tell that from "we could not see Live".
func TestDescribePlanBasis_NoAppliedBundleIsNamed(t *testing.T) {
	t.Parallel()
	got := describePlanBasis(release.PlanBasis{})
	if !strings.Contains(got, "no applied bundle") {
		t.Errorf("an absent applied bundle must be named, not rendered blank; got %q", got)
	}
	drifted := describePlanBasis(release.PlanBasis{AppliedBundleID: "bnd_1", DriftObserved: true})
	if !strings.Contains(drifted, "DRIFT") {
		t.Errorf("observed drift is part of what the plan was computed against; got %q", drifted)
	}
}

// TestPlanSource_F15FallsBackWhenTheServerPredatesBundles is F-15.
//
// A real control plane today answers Unimplemented on PlanDeploy, because the
// server handlers are cp task C4a and have not landed. That is NOT a refusal
// of this deploy — it is a server that cannot compute a plan at all — so forge
// says what the doc says to say and falls back to the pre-bundle path, where
// the confirmation gate alone stands in front of the write.
//
// The branch is recognised by F4's TYPED error, never by message text: an old
// server's prose is not a contract, and one that improved its wording would
// silently stop matching and start failing deploys.
func TestPlanSource_F15FallsBackWhenTheServerPredatesBundles(t *testing.T) {
	t.Parallel()
	unimplemented := bundlesUnsupported(&cloud.Error{
		Code: cloud.CodeUnimplemented, Procedure: procPlanDeploy,
	})
	if !errors.Is(unimplemented, errControlPlanePredatesBundles) {
		t.Fatal("precondition: an Unimplemented answer must map to the F-15 typed error")
	}

	var out strings.Builder
	plan, handled := planUnavailable(unimplemented, "prod", &out)
	if !handled {
		t.Fatal("F-15 must be handled as a fallback, not returned as a deploy failure")
	}
	if plan != nil {
		t.Error("there is no plan to fall back TO: a nil plan is the honest answer")
	}
	msg := out.String()
	// The doc specifies the sentence, because it is what tells an operator
	// the remedy is an upgrade rather than a retry.
	if !strings.Contains(msg, "predates bundles") || !strings.Contains(msg, "upgrade") {
		t.Errorf("the fallback must say the control plane predates bundles and must be upgraded; got: %s", msg)
	}
}

// TestPlanSource_ARealFailureIsNotAnF15Fallback: only Unimplemented falls
// back. A permission error, a timeout or an InvalidArgument is a real failure
// and must NOT be silently converted into "no plan" — that would turn every
// transient control-plane problem into an ungated deploy.
func TestPlanSource_ARealFailureIsNotAnF15Fallback(t *testing.T) {
	t.Parallel()
	for name, code := range map[string]string{
		"permission denied": cloud.CodePermissionDenied,
		"unavailable":       cloud.CodeUnavailable,
		"invalid argument":  cloud.CodeInvalidArgument,
	} {
		err := bundlesUnsupported(&cloud.Error{Code: code, Procedure: procPlanDeploy})
		if _, handled := planUnavailable(err, "prod", io.Discard); handled {
			t.Errorf("%s must not be read as F-15: only a server that cannot hold the record at all falls back", name)
		}
	}
}
