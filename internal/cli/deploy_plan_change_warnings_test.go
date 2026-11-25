package cli

// The plan's Changes warnings (owner decision 6, 2026-11-25): a deploy whose
// release no longer declares what a running Change delivers goes through, and
// the control plane stops that Change. The plan says so first. These pin the
// three things that make that a WARNING and nothing more: it is decoded from
// beside the plan, never inside its digest; a control plane that predates it
// is not an error; and it is printed and carried in --json without touching
// the gate.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestPlanDeploy_DecodesChangeWarningsBesideThePlan(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)
	fake := &fakeDSOTCaller{replies: map[string]any{
		procPlanDeploy: map[string]any{
			"plan": planToWireFixture(plan),
			"changeWarnings": []any{
				map[string]any{
					"changeId": "chg_1", "key": "hero-copy", "mode": "experiment", "runId": "r_1",
					"undeclaredVariants": []any{"v4"}, "detail": "variant v4 is no longer declared",
				},
				map[string]any{"changeId": "chg_2", "key": "old-banner", "mode": "everyone", "keyUndeclared": true},
			},
		},
	}}
	got, err := hostedPlanClient{client: fake}.PlanDeploy(context.Background(), "env_prod", "bnd_1", "v1.4.0")
	if err != nil {
		t.Fatalf("a plan with change warnings must decode: %v", err)
	}
	// The digest is the plan's alone: the warnings beside it must not move
	// it, or a Change launched between plan and approval would make every
	// approval stale.
	if got.Digest != plan.Digest {
		t.Errorf("digest = %s, want the plan's own %s", got.Digest, plan.Digest)
	}
	if ok, verr := got.VerifyDigest(); verr != nil || !ok {
		t.Errorf("the plan must still verify with warnings attached: ok=%v err=%v", ok, verr)
	}
	want := []release.PlanChangeWarning{
		{ChangeID: "chg_1", Key: "hero-copy", Mode: "experiment", RunID: "r_1",
			UndeclaredVariants: []string{"v4"}, Detail: "variant v4 is no longer declared"},
		{ChangeID: "chg_2", Key: "old-banner", Mode: "everyone", KeyUndeclared: true},
	}
	if len(got.ChangeWarnings) != len(want) {
		t.Fatalf("change warnings = %+v, want %+v", got.ChangeWarnings, want)
	}
	for i := range want {
		g, w := got.ChangeWarnings[i], want[i]
		if g.ChangeID != w.ChangeID || g.Key != w.Key || g.Mode != w.Mode || g.RunID != w.RunID ||
			strings.Join(g.UndeclaredVariants, ",") != strings.Join(w.UndeclaredVariants, ",") ||
			g.KeyUndeclared != w.KeyUndeclared || g.Detail != w.Detail {
			t.Errorf("warning %d = %+v, want %+v", i, g, w)
		}
	}
}

// An older control plane omits the field: no warning, no error.
func TestPlanDeploy_NoChangeWarningsFromAnOlderControlPlane(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)
	fake := &fakeDSOTCaller{replies: map[string]any{
		procPlanDeploy: map[string]any{"plan": planToWireFixture(plan)},
	}}
	got, err := hostedPlanClient{client: fake}.PlanDeploy(context.Background(), "env_prod", "bnd_1", "v1.4.0")
	if err != nil {
		t.Fatalf("a plan without change warnings must decode: %v", err)
	}
	if got.ChangeWarnings != nil {
		t.Errorf("change warnings = %+v, want none", got.ChangeWarnings)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "change_warnings") {
		t.Errorf("a plan with no warnings must omit change_warnings from its JSON: %s", raw)
	}
}

func TestDescribeChangeWarning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		w    release.PlanChangeWarning
		want string
	}{
		{"one variant", release.PlanChangeWarning{Key: "hero-copy", Mode: "experiment", RunID: "r_1", UndeclaredVariants: []string{"v4"}},
			"change hero-copy (experiment, run r_1) uses variant v4, which this release no longer declares: deploying stops it (everyone gets the default)."},
		{"two variants, no run", release.PlanChangeWarning{Key: "hero-copy", Mode: "rollout", UndeclaredVariants: []string{"v3", "v4"}},
			"change hero-copy (rollout) uses variants v3, v4, which this release no longer declares: deploying stops it (everyone gets the default)."},
		{"key undeclared", release.PlanChangeWarning{Key: "old-banner", Mode: "everyone", KeyUndeclared: true},
			"change old-banner (everyone) is running, but this release no longer declares change old-banner: deploying stops it (everyone gets the default)."},
		{"unknown reason falls back to detail", release.PlanChangeWarning{Key: "k", Detail: "a newer reason"},
			"change k: a newer reason"},
		{"no key names the id", release.PlanChangeWarning{ChangeID: "chg_9"},
			"change chg_9 is running: deploying stops it (everyone gets the default)."},
	}
	for _, tc := range cases {
		if got := describeChangeWarning(tc.w); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, got, tc.want)
		}
	}
}

// The warnings print in the plan, warn-classed, and change nothing about the
// gate: no acknowledgement, no refusal.
func TestRenderDeployPlan_PrintsChangeWarningsWithoutGating(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)
	plan.ChangeWarnings = []release.PlanChangeWarning{
		{ChangeID: "chg_1", Key: "hero-copy", Mode: "experiment", RunID: "r_1", UndeclaredVariants: []string{"v4"}},
	}
	var out bytes.Buffer
	renderDeployPlan(&out, "prod", plan)
	want := "    [warn] change hero-copy (experiment, run r_1) uses variant v4, which this release no longer declares: deploying stops it (everyone gets the default).\n"
	if !strings.Contains(out.String(), "running changes this deploy stops\n"+want) {
		t.Errorf("the plan must print the warning under its heading; got:\n%s", out.String())
	}
	// Every stop finding acknowledged: the warning must add nothing to
	// acknowledge.
	if err := checkPlanApproval("prod", plan, deployApproval{AcknowledgedFindings: plan.StopCodes()}, true); err != nil {
		t.Errorf("a change warning must never refuse a deploy: %v", err)
	}
}

// A plan whose only news is a stopped Change must not also say "no changes
// found".
func TestRenderDeployPlan_ChangeWarningIsNotNoChanges(t *testing.T) {
	t.Parallel()
	plan := release.Plan{Digest: "sha256:" + rep64('a'), Findings: []release.PlanFinding{},
		ChangeWarnings: []release.PlanChangeWarning{{Key: "old-banner", KeyUndeclared: true}}}
	var out bytes.Buffer
	renderDeployPlan(&out, "prod", plan)
	if strings.Contains(out.String(), "no changes found") {
		t.Errorf("a plan that stops a running change must not say no changes found:\n%s", out.String())
	}
}

// --json carries them on the plan document, as deploy_plan.change_warnings.
func TestPromotePlanJSON_CarriesChangeWarnings(t *testing.T) {
	t.Parallel()
	plan := buildTestPlan(t)
	plan.ChangeWarnings = []release.PlanChangeWarning{
		{ChangeID: "chg_1", Key: "hero-copy", Mode: "experiment", RunID: "r_1", UndeclaredVariants: []string{"v4"}},
	}
	raw, err := json.Marshal(promotePlan{DeployPlan: &plan})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		DeployPlan struct {
			ChangeWarnings []map[string]any `json:"change_warnings"`
		} `json:"deploy_plan"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.DeployPlan.ChangeWarnings) != 1 {
		t.Fatalf("deploy_plan.change_warnings missing from the document: %s", raw)
	}
	w := doc.DeployPlan.ChangeWarnings[0]
	for k, v := range map[string]any{"change_id": "chg_1", "key": "hero-copy", "mode": "experiment", "run_id": "r_1"} {
		if w[k] != v {
			t.Errorf("change_warnings[0].%s = %v, want %v", k, w[k], v)
		}
	}
	if vs, _ := w["undeclared_variants"].([]any); len(vs) != 1 || vs[0] != "v4" {
		t.Errorf("change_warnings[0].undeclared_variants = %v, want [v4]", w["undeclared_variants"])
	}
}
