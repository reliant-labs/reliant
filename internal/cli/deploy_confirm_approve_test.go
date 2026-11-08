package cli

// --plan-only must hand back the command that approves THE PLAN IT JUST
// PRINTED, not a generic --yes.
//
// These are mutation-checked against the digest branch of approveCommand:
// deleting the `--approve <digest>` append, or dropping the stop-class codes,
// turns the first two tests red rather than leaving them passing on a
// substring that both forms share. That is why they assert on the --approve
// and --acknowledge-destructive text explicitly and ALSO assert the --yes
// form is absent — a test that only looked for "forge env deploy prod v1"
// would pass against the bug this fixes.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// planOnlyOutput runs the gate in --plan-only mode and returns what it
// printed plus the outcome.
func planOnlyOutput(t *testing.T, env string, plan promotePlan, c deployConfirm) (string, deployConfirmOutcome) {
	t.Helper()
	var buf bytes.Buffer
	c.PlanOnly = true
	c.out = &buf
	outcome := confirmDeployPlan(env, plan, c, false)
	if outcome.Err != nil {
		t.Fatalf("--plan-only must not error: %v", outcome.Err)
	}
	if outcome.Confirmed {
		t.Fatal("--plan-only confirmed the deploy")
	}
	return buf.String(), outcome
}

// Stage two of the documented pipeline is `--approve <digest>` with no --yes
// and no terminal. It must PROCEED: before this, the gate only knew --yes and a
// TTY, so the exact command --plan-only printed refused with exit 5
// plan_unconfirmed (observed deploying control-plane v1.7.33 to prod).
func TestConfirm_ApproveMatchingDigestConfirmsWithoutTTY(t *testing.T) {
	plan := promotePlan{Env: "prod", DeployPlan: &release.Plan{Digest: "sha256:abc123"}}
	var buf bytes.Buffer
	outcome := confirmDeployPlan("prod", plan, deployConfirm{Approve: "sha256:abc123", Interactive: false, out: &buf}, false)
	if outcome.Err != nil || !outcome.Confirmed {
		t.Fatalf("--approve naming this plan must confirm, got confirmed=%v err=%v", outcome.Confirmed, outcome.Err)
	}
}

// --approve is consent for ONE plan. A different digest, or no plan at all,
// is never consent — whichever gate a future caller happens to run first.
func TestConfirm_ApproveIsNotConsentForAnotherPlan(t *testing.T) {
	for name, plan := range map[string]promotePlan{
		"other digest": {Env: "prod", DeployPlan: &release.Plan{Digest: "sha256:def456"}},
		"no plan":      {Env: "prod"},
	} {
		var buf bytes.Buffer
		outcome := confirmDeployPlan("prod", plan, deployConfirm{Approve: "sha256:abc123", out: &buf}, false)
		if outcome.Confirmed {
			t.Fatalf("%s: --approve sha256:abc123 confirmed a plan it did not name", name)
		}
	}
}

// With a plan digest, the approve command is --approve <digest> — the flag
// that binds the approval to the change set that was read.
func TestPlanOnly_NamesApproveWithTheDigest(t *testing.T) {
	plan := promotePlan{
		Env:        "prod",
		Target:     promotePlanTarget{Release: "v1.4.0"},
		DeployPlan: &release.Plan{Digest: "sha256:abc123"},
	}
	got, outcome := planOnlyOutput(t, "prod", plan, deployConfirm{})

	want := "forge env deploy prod v1.4.0 --approve sha256:abc123"
	if !strings.Contains(got, want) {
		t.Errorf("--plan-only must hand back %q, got:\n%s", want, got)
	}
	// The bug: handing back --yes, which approves whatever forge recomputes
	// instead of the plan the operator read.
	if strings.Contains(got, "--yes") {
		t.Errorf("--plan-only named --yes while a plan digest existed; got:\n%s", got)
	}
	// next_step and the printed line are ONE string. If they are built
	// twice they drift, and a pipeline cannot tell which is authoritative.
	if outcome.NextStep != want {
		t.Errorf("next_step = %q, want %q", outcome.NextStep, want)
	}
	if !strings.Contains(got, outcome.NextStep) {
		t.Errorf("next_step %q is not the command that was printed:\n%s", outcome.NextStep, got)
	}
}

// Stop-class findings are listed BY CODE in the same command: the code is not
// knowable before the plan is computed, so the stage that computed it is the
// only place that can name them.
func TestPlanOnly_ListsExactlyTheStopClassCodes(t *testing.T) {
	plan := promotePlan{
		Env:    "prod",
		Target: promotePlanTarget{Release: "v1.4.0"},
		DeployPlan: &release.Plan{
			Digest: "sha256:abc123",
			Findings: []release.PlanFinding{
				{Code: "stateful_deletion", Class: release.ClassStop, Subject: "k/StatefulSet/ns/db"},
				{Code: "image_changed", Class: release.ClassInfo, Subject: "api"},
				{Code: "lb_identity_change", Class: release.ClassStop, Subject: "k/Service/ns/edge"},
				// A second finding of an already-named code must not
				// produce a duplicate: acknowledgement is by code.
				{Code: "stateful_deletion", Class: release.ClassStop, Subject: "k/StatefulSet/ns/cache"},
			},
		},
	}
	got, outcome := planOnlyOutput(t, "prod", plan, deployConfirm{})

	want := "forge env deploy prod v1.4.0 --approve sha256:abc123 " +
		"--acknowledge-destructive lb_identity_change,stateful_deletion"
	if outcome.NextStep != want {
		t.Errorf("next_step = %q, want %q", outcome.NextStep, want)
	}
	if !strings.Contains(got, want) {
		t.Errorf("--plan-only must list exactly the stop codes; want %q, got:\n%s", want, got)
	}
	// An info-class finding is not something to acknowledge, and listing
	// it would train the operator to paste codes without reading them.
	if strings.Contains(outcome.NextStep, "image_changed") {
		t.Errorf("an info-class code was listed as destructive: %q", outcome.NextStep)
	}
}

// A plan with no stop-class findings gets no --acknowledge-destructive. The
// flag means "I have decided about a specific destructive change"; emitting
// it unconditionally would make it boilerplate.
func TestPlanOnly_OmitsAcknowledgeWhenNothingIsDestructive(t *testing.T) {
	plan := promotePlan{
		Env:    "prod",
		Target: promotePlanTarget{Release: "v1.4.0"},
		DeployPlan: &release.Plan{
			Digest:   "sha256:abc123",
			Findings: []release.PlanFinding{{Code: "image_changed", Class: release.ClassInfo, Subject: "api"}},
		},
	}
	_, outcome := planOnlyOutput(t, "prod", plan, deployConfirm{})
	if strings.Contains(outcome.NextStep, "--acknowledge-destructive") {
		t.Errorf("no stop-class finding, but the command names one: %q", outcome.NextStep)
	}
}

// ONLY a nil plan falls back to --yes, and it says why. Nil means the plan is
// UNAVAILABLE (a never-built env, or a control plane that predates bundles),
// which is not "no changes" — so the fallback must not read like the normal
// answer.
func TestPlanOnly_FallsBackToYesOnlyWithNoPlanAndSaysWhy(t *testing.T) {
	plan := promotePlan{Env: "prod", Target: promotePlanTarget{Release: "v1.4.0"}}
	got, outcome := planOnlyOutput(t, "prod", plan, deployConfirm{})

	want := "forge env deploy prod v1.4.0 --yes"
	if outcome.NextStep != want {
		t.Errorf("next_step = %q, want %q", outcome.NextStep, want)
	}
	if !strings.Contains(got, want) {
		t.Errorf("a planless env must get the --yes form; got:\n%s", got)
	}
	// It must explain itself. Without this the reader cannot tell the
	// fallback from the normal path, which is the F-20 distinction.
	if !strings.Contains(got, "No plan digest to approve") {
		t.Errorf("the --yes fallback did not say why it is the fallback:\n%s", got)
	}

	// A plan present but carrying an empty digest is the same state:
	// there is nothing to bind an approval to.
	plan.DeployPlan = &release.Plan{}
	_, outcome = planOnlyOutput(t, "prod", plan, deployConfirm{})
	if outcome.NextStep != want {
		t.Errorf("an empty digest must fall back too: next_step = %q, want %q", outcome.NextStep, want)
	}
}

// A versionless --plan-only names the version it CUT, not the plan's target —
// that release exists and its images are pushed, so approving it must not
// send the caller back to the form that builds.
func TestPlanOnly_ApproveCommandNamesTheAutoCutVersion(t *testing.T) {
	plan := promotePlan{
		Env:        "prod",
		Target:     promotePlanTarget{Release: "20260701.120000-abc123def456"},
		DeployPlan: &release.Plan{Digest: "sha256:abc123"},
	}
	got, outcome := planOnlyOutput(t, "prod", plan, deployConfirm{AutoVersion: "20260701.120000-abc123def456"})
	want := "forge env deploy prod 20260701.120000-abc123def456 --approve sha256:abc123"
	if outcome.NextStep != want {
		t.Errorf("next_step = %q, want %q", outcome.NextStep, want)
	}
	if !strings.Contains(got, "was cut and pushed") {
		t.Errorf("a versionless --plan-only must say the release was already cut:\n%s", got)
	}
}

// R3's contract: a versionless --plan-only's document carries the auto
// version in target.release, so the daemon reads the version to approve from
// the same field a versioned deploy uses. No separate auto_version field is
// needed, and this test is what keeps that true.
//
// It also pins that the plan's next_step is REPLACED with the approve command
// on the --plan-only path (the default is "forge env deploy <env>", which
// approves nothing).
func TestPlanOnly_DocumentCarriesAutoVersionAndApproveNextStep(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the deploy path end to end; skipped in -short")
	}
	dir := deployAllProject(t)
	t.Chdir(dir)
	stubDeployBuild(t, dir, map[string]string{"api": sha("1")})
	ledger := testLedger(t, dir)

	cut, err := buildAndCutForDeploy(context.Background(), dir, "prod", deployCmdFlags{}, ledger)
	if err != nil {
		t.Fatalf("build and cut: %v", err)
	}

	// --json: the document goes to stdout, the rendered plan to stderr.
	var runErr error
	stdout := captureStdout(t, func() {
		runErr = runPromote(context.Background(), cut.Version, "prod", promoteOptions{
			Ledger: ledger, ProjectDir: dir, JSON: true,
			Confirm:    &deployConfirm{PlanOnly: true, AutoVersion: cut.Version, Interactive: false},
			DeployPlan: &release.Plan{Digest: "sha256:deadbeef"},
		})
	})
	if runErr != nil {
		t.Fatalf("--plan-only must exit 0, got: %v", runErr)
	}

	var doc struct {
		Target     struct{ Release string } `json:"target"`
		NextStep   string                   `json:"next_step"`
		Confirmed  bool                     `json:"confirmed"`
		Applied    bool                     `json:"applied"`
		DeployPlan *release.Plan            `json:"deploy_plan"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document (%v):\n%s", err, stdout)
	}

	if doc.Target.Release != cut.Version {
		t.Errorf("target.release = %q, want the auto-cut version %q — R3 reads the "+
			"version to approve from this field", doc.Target.Release, cut.Version)
	}
	want := "forge env deploy prod " + cut.Version + " --approve sha256:deadbeef"
	if doc.NextStep != want {
		t.Errorf("next_step = %q, want %q", doc.NextStep, want)
	}
	// The digest a pipeline reads to build that command itself.
	if doc.DeployPlan == nil || doc.DeployPlan.Digest != "sha256:deadbeef" {
		t.Errorf("deploy_plan.digest missing from the --plan-only document: %+v", doc.DeployPlan)
	}
	if doc.Confirmed || doc.Applied {
		t.Errorf("--plan-only reported confirmed=%v applied=%v; both must be false",
			doc.Confirmed, doc.Applied)
	}
}
