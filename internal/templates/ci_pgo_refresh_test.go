package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// pgo-refresh.yml, as rendered: the weekly pull of an env's CPU profiles into
// each binary's committed default.pgo. Asserted on the workflow text, parsed
// as YAML, because that is all a user gets.

// It runs on a schedule and by hand, never on a push: a profile is data a
// person merges, not something a commit triggers.
func TestPGORefresh_RunsWeeklyAndByHand(t *testing.T) {
	out := renderCIText(t, "pgo-refresh.yml.tmpl", PGORefreshWorkflowData{ProjectName: "demo", Env: "prod"})
	events, _, _ := workflowEvents(t, "pgo-refresh.yml", []byte(out))
	if !events["schedule"] || !events["workflow_dispatch"] || len(events) != 2 {
		t.Errorf("pgo-refresh.yml triggers = %v, want exactly schedule and workflow_dispatch", events)
	}
}

// It pulls the env it was rendered for, holds only its read credential (in
// the one variable forge reads), never the deploy token, and documents the
// scopes that credential needs (internal/cli TestPGORefreshTokenScopesCoverPGOPull
// proves them against the calls the pull makes).
func TestPGORefresh_PullsTheEnvWithAnObsToken(t *testing.T) {
	out := renderCIText(t, "pgo-refresh.yml.tmpl", PGORefreshWorkflowData{ProjectName: "demo", Env: "prod"})
	if !strings.Contains(out, "forge pgo pull --env prod --json") {
		t.Error("pgo-refresh.yml must run `forge pgo pull --env prod --json`")
	}
	if !strings.Contains(out, "FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_OBS_TOKEN }}") {
		t.Error("pgo-refresh.yml must hand forge the FORGE_OBS_TOKEN secret under the variable forge reads")
	}
	if strings.Contains(out, "secrets.FORGE_CONTROL_PLANE_TOKEN") {
		t.Error("pgo-refresh.yml reads profiles; it must not hold the deploy token")
	}
	if !strings.Contains(out, "FORGE_OBS_TOKEN, a token with "+PGORefreshTokenScopes) {
		t.Errorf("pgo-refresh.yml must document the token's scopes, %s", PGORefreshTokenScopes)
	}
}

// Without the secret the job ends green: every step but the notice is gated
// on the token, so a project that has not minted one is never red.
func TestPGORefresh_SkipsCleanlyWithoutTheSecret(t *testing.T) {
	out := renderCIText(t, "pgo-refresh.yml.tmpl", PGORefreshWorkflowData{ProjectName: "demo", Env: "prod"})
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				If   string `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	job, ok := doc.Jobs["refresh"]
	if !ok || len(job.Steps) < 2 {
		t.Fatalf("pgo-refresh.yml has no refresh job with steps: %+v", doc.Jobs)
	}
	if job.Steps[0].If != "env.FORGE_CONTROL_PLANE_TOKEN == ''" {
		t.Errorf("the first step must be the no-token notice, got %+v", job.Steps[0])
	}
	for _, s := range job.Steps[1:] {
		if s.If != "env.FORGE_CONTROL_PLANE_TOKEN != ''" {
			t.Errorf("step %q (%s) runs without the token; it must be gated on it", s.Name, s.Uses)
		}
	}
}

// Only files the pull wrote are committed, and only when their bytes moved:
// an unchanged profile opens no pull request, and nothing else in the
// checkout (the pull's own JSON, the PR body) is ever added.
func TestPGORefresh_CommitsOnlyChangedProfiles(t *testing.T) {
	out := renderCIText(t, "pgo-refresh.yml.tmpl", PGORefreshWorkflowData{ProjectName: "demo", Env: "prod"})
	for _, want := range []string{
		`select(.action == "written") | .path`,
		`git diff --quiet -- "${written[@]}"`,
		`git add -- "${written[@]}"`,
		`gh pr create --head "$BRANCH"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pgo-refresh.yml lacks %q", want)
		}
	}
	if strings.Contains(out, "git add -A") || strings.Contains(out, "git add .") {
		t.Error("pgo-refresh.yml must add only the profiles the pull wrote")
	}
}
