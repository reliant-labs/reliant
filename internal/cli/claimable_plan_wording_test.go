package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// A real claimable deploy never calls itself a dry run, never says nothing was
// written, and its --json never says it ships nothing; the plan a --plan-only
// run prints still says dry run.
func TestClaimableDeployNeverClaimsToBeADryRun(t *testing.T) {
	testkit.Slow(t, "renders KCL and runs the whole deploy")
	claimableFixture(t)
	text, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, text)
	}
	for _, bad := range []string{"dry run", "NOTHING WRITTEN", "SHIPS NOTHING"} {
		if strings.Contains(text, bad) {
			t.Errorf("a real deploy's output says %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "PLAN (approved") {
		t.Errorf("the plan a real deploy prints does not say it is approved:\n%s", text)
	}

	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json")
	if err != nil {
		t.Fatalf("deploy --json: %v\n%s", err, out)
	}
	doc := decodeClaimDoc(t, out)
	note, _ := doc["note"].(string)
	if strings.Contains(note, "ships nothing") || strings.Contains(note, "POINTER") || doc["ships_nothing"] != false || doc["dry_run"] != false {
		t.Errorf("--json of a real deploy: ships_nothing=%v dry_run=%v note=%q", doc["ships_nothing"], doc["dry_run"], note)
	}
}

func TestPlanHeadlineWordingPerStage(t *testing.T) {
	plan := promotePlan{Env: "prod", Target: promotePlanTarget{Release: "v1"}}
	render := func(p planPending) string {
		plan.pending = p
		var b strings.Builder
		renderPromotePlanText(&b, plan)
		return b.String()
	}
	if got := render(planNotPending); !strings.Contains(got, "PLAN (dry run — nothing was written)") || !strings.Contains(got, "NOTHING WRITTEN") {
		t.Errorf("--plan / --plan-only must still say dry run:\n%s", got)
	}
	for _, p := range []planPending{planApproved, planAwaitingApproval} {
		got := render(p)
		for _, bad := range []string{"dry run", "NOTHING WRITTEN", "Deploy:   forge env deploy"} {
			if strings.Contains(got, bad) {
				t.Errorf("pending %d plan says %q:\n%s", p, bad, got)
			}
		}
	}
	plan.pending = planNotPending
	plan.recordFollow(&shipLog{}, nil)
	if strings.Contains(plan.Note, "ships nothing") || !strings.Contains(plan.Note, "control plane applies it") {
		t.Errorf("note after a follow-through = %q", plan.Note)
	}
}
