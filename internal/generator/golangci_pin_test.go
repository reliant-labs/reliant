package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// golangci-lint is installed in six places: forge's own CI (ci.yml's two
// lint steps, e2e-suite.yml), forge's own scripts/bootstrap.sh, and
// the two copies forge SHIPS — the scaffolded CI workflow and the scaffolded
// scripts/bootstrap.sh. Every one of them must name the same explicit
// version, templates.GolangciLintVersion.
//
// WHY A PIN, NOT `latest`. On 2026-09-24 golangci-lint v2.14.0 shipped a
// gocritic check (sprintfQuotedString) that fired on seven lines untouched
// for months. forge's main stayed green only because the lint job ran with
// only-new-issues, which lints the push diff — the repo had silently stopped
// passing its own linter, and the first PR to touch one of those files would
// have gone red for a reason unrelated to it. `latest` means the linter
// changes without a commit, so a green main proves nothing about tomorrow.
//
// WHY THE SAME PIN EVERYWHERE. `forge lint` runs whatever golangci-lint is on
// PATH. If the bootstrap, forge's CI and the scaffold's CI disagree, "lint is
// clean" on a laptop and in CI are claims about two different linters.
//
// WHY THE MODULE PATH IS CHECKED. golangci-lint v2 lives at
// github.com/golangci/golangci-lint/v2. `go install
// github.com/golangci/golangci-lint/cmd/golangci-lint@latest` — what both
// bootstrap scripts used to say — resolves the v1 module (v1.64.8), which
// cannot read the `version: "2"` .golangci.yml forge scaffolds.
//
// WHY THE ACTION MAJOR IS CHECKED. golangci-lint-action v6 refuses
// golangci-lint v2 outright ("golangci-lint v2 is not supported by
// golangci-lint-action v6"), and given `version: latest` it installs v1.64.8
// instead. The scaffold shipped exactly that, so no scaffolded project's Lint
// job could pass.
func TestGolangciLintVersionIsPinnedEverywhere(t *testing.T) {
	want := templates.GolangciLintVersion
	if !regexp.MustCompile(`^v2\.\d+\.\d+$`).MatchString(want) {
		t.Fatalf("templates.GolangciLintVersion = %q, want an explicit v2.X.Y release", want)
	}

	root := forgeRepoRoot(t)
	workflows := map[string][]byte{}
	for _, rel := range []string{
		".github/workflows/ci.yml",
		".github/workflows/e2e-suite.yml",
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		workflows["forge repo: "+rel] = raw
	}
	ci, err := templates.CITemplates("github").Render("ci.yml.tmpl", templates.CIWorkflowData{
		ProjectName:  "pin-probe",
		LintGolangci: true,
		PermContents: "read",
		Module:       "example.com/pin-probe",
	})
	if err != nil {
		t.Fatalf("render ci.yml.tmpl: %v", err)
	}
	workflows["scaffold: .github/workflows/ci.yml"] = ci

	scripts := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "bootstrap.sh"))
	if err != nil {
		t.Fatal(err)
	}
	scripts["forge repo: scripts/bootstrap.sh"] = string(raw)
	dir := t.TempDir()
	if err := NewProjectGenerator("pin-probe", dir, "example.com/pin-probe").generateBootstrapScript(); err != nil {
		t.Fatalf("generateBootstrapScript: %v", err)
	}
	if raw, err = os.ReadFile(filepath.Join(dir, "scripts", "bootstrap.sh")); err != nil {
		t.Fatal(err)
	}
	scripts["scaffold: scripts/bootstrap.sh"] = string(raw)

	for site, body := range workflows {
		if n := checkWorkflowGolangciPin(t, site, body, want); n == 0 {
			t.Errorf("%s: no golangci-lint install found — the pin guard inspected nothing here", site)
		}
	}
	for site, body := range scripts {
		if n := checkScriptGolangciPin(t, site, body, want); n == 0 {
			t.Errorf("%s: no golangci-lint install found — the pin guard inspected nothing here", site)
		}
	}
}

var (
	golangciActionRE = regexp.MustCompile(`^golangci/golangci-lint-action@v(\d+)`)
	golangciGoInstRE = regexp.MustCompile(`github\.com/golangci/golangci-lint(/v\d+)?/cmd/golangci-lint@(\S+)`)
	// The version is the LAST argument on the `| sh -s -- -b <dir> <version>`
	// line; <dir> is usually "$(go env GOPATH)/bin", which contains spaces.
	golangciInstallRE = regexp.MustCompile(`golangci-lint/\S+/install\.sh[\s\S]*?\|\s*sh -s -- -b [^\n]*\s(\S+)[ \t]*(?:\n|$)`)
)

// checkWorkflowGolangciPin checks every golangci-lint install step in a
// workflow and returns how many it found.
func checkWorkflowGolangciPin(t *testing.T, site string, body []byte, want string) int {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string         `yaml:"uses"`
				Run  string         `yaml:"run"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("%s: parse: %v", site, err)
	}
	installs := 0
	for job, j := range wf.Jobs {
		for _, s := range j.Steps {
			if m := golangciActionRE.FindStringSubmatch(s.Uses); m != nil {
				installs++
				if major, _ := strconv.Atoi(m[1]); major < 7 {
					t.Errorf("%s (job %s): %s cannot run golangci-lint v2 — use v7 or later", site, job, s.Uses)
				}
				if got, _ := s.With["version"].(string); got != want {
					t.Errorf("%s (job %s): golangci-lint-action `version: %q`, want %s", site, job, got, want)
				}
				if newOnly, _ := s.With["only-new-issues"].(bool); newOnly {
					t.Errorf("%s (job %s): only-new-issues lints the diff, not the repo — a repo that stops "+
						"passing its own linter stays green until an unrelated PR touches the file", site, job)
				}
			}
			for _, m := range golangciInstallRE.FindAllStringSubmatch(s.Run, -1) {
				installs++
				if m[1] != want {
					t.Errorf("%s (job %s): install.sh installs %q, want %s", site, job, m[1], want)
				}
			}
			installs += checkScriptGolangciPin(t, site+" (job "+job+")", s.Run, want)
		}
	}
	return installs
}

// checkScriptGolangciPin checks every `go install` of golangci-lint in a
// shell script and returns how many it found.
func checkScriptGolangciPin(t *testing.T, site, body, want string) int {
	t.Helper()
	matches := golangciGoInstRE.FindAllStringSubmatch(body, -1)
	for _, m := range matches {
		if m[1] != "/v2" {
			t.Errorf("%s: %q is the golangci-lint v1 module path — v2 is "+
				"github.com/golangci/golangci-lint/v2/cmd/golangci-lint", site, m[0])
		}
		if m[2] != want {
			t.Errorf("%s: %q, want @%s", site, m[0], want)
		}
	}
	if strings.Contains(body, "golangci-lint@latest") {
		t.Errorf("%s: installs golangci-lint@latest", site)
	}
	return len(matches)
}
