//go:build e2e

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2ENewProjectHasNoUnprovenSignupFinding is the regression gate for
// forge bug 90412992: forgeconv-unproven-signup read the env projection forge
// generates into every deploy/kcl/config_gen.k as sign-up turned on outside
// development, so once a project pinned the release the rule gates from, a
// project `forge project new` had just written failed its own `forge lint`.
//
// TestE2EFreshScaffoldLintExitsZero proves a scaffold lints clean at the pin
// it was born with. This proves the sign-up lane in particular stays clean at
// EVERY later pin: forge_version is moved past every rollout row's GatesFrom,
// and the assertion is on findings of any severity, so a finding that only
// warns today (and would gate in a later release) fails it as well. Then a
// real legacy sign-up is declared in prod, and the same run must report it as
// a gating error: the lane ran, so the clean result is not vacuous.
func TestE2ENewProjectHasNoUnprovenSignupFinding(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	for _, spec := range []sharedProjectSpec{dashboardCatalogProjectE2E, widgetServiceProjectE2E} {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			projectDir := newProjectFromSharedE2E(t, forgeBin, spec)
			if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", "config_gen.k")); err != nil {
				t.Fatalf("the scaffold has no deploy/kcl/config_gen.k, the file the bug was in: %v", err)
			}
			pinPastEveryGate(t, projectDir)

			if fs := unprovenSignupFindingsE2E(t, forgeBin, projectDir); len(fs) != 0 {
				t.Fatalf("a new project has unproven-signup findings: %+v", fs)
			}

			prod := filepath.Join(projectDir, "deploy", "kcl", "prod", "main.k")
			body := readFileE2E(t, prod) + "\n_legacy_signup_probe = {\"AUTH_SELF_SIGNUP\" = \"true\"}\n"
			if err := os.WriteFile(prod, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			fs := unprovenSignupFindingsE2E(t, forgeBin, projectDir)
			if len(fs) != 1 || fs[0].Rule != "forgeconv-unproven-signup" || fs[0].File != "deploy/kcl/prod/main.k" || fs[0].Severity != "error" {
				t.Fatalf("a legacy sign-up declared in prod = %+v; want one gating forgeconv-unproven-signup error in deploy/kcl/prod/main.k", fs)
			}
		})
	}
}

// pinPastEveryGate moves the project's forge_version past every GatesFrom the
// rollout table declares, so every rule grades as gating.
func pinPastEveryGate(t *testing.T, projectDir string) {
	t.Helper()
	path := filepath.Join(projectDir, "forge.yaml")
	raw := readFileE2E(t, path)
	re := regexp.MustCompile(`(?m)^forge_version:.*$`)
	if !re.MatchString(raw) {
		t.Fatalf("forge.yaml has no forge_version line:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(re.ReplaceAllString(raw, "forge_version: v1.999.0")), 0o644); err != nil {
		t.Fatal(err)
	}
}

type lintFindingE2E struct {
	File     string `json:"file"`
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
}

// unprovenSignupFindingsE2E runs `forge lint --json` scoped to deploy/kcl
// (the lane is per-file; scoping skips the whole-project lanes this test is
// not about) and returns its findings of the unproven-signup rules.
func unprovenSignupFindingsE2E(t *testing.T, forgeBin, projectDir string) []lintFindingE2E {
	t.Helper()
	// A non-zero exit is expected when the probe is declared; the JSON is
	// the verdict either way.
	out, _ := runLintE2E(t, projectDir, filepath.Dir(forgeBin), forgeBin, "lint", "--json", "--scope", "deploy/kcl")
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("forge lint --json printed no JSON:\n%s", out)
	}
	var doc struct {
		Findings []lintFindingE2E `json:"findings"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&doc); err != nil {
		t.Fatalf("decode forge lint --json: %v\n%s", err, out)
	}
	var mine []lintFindingE2E
	for _, f := range doc.Findings {
		if strings.HasPrefix(f.Rule, "forgeconv-unproven-signup") {
			mine = append(mine, f)
		}
	}
	return mine
}
