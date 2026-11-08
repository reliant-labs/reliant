package lint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardConfigYAML is the forbidigo block forge scaffolds into .golangci.yml
// (templates/project/golangci.yml.tmpl), in warn mode: settings present,
// forbidigo absent from linters.enable.
const guardConfigYAML = `version: "2"
linters:
  settings:
    forbidigo:
      forbid:
        - pattern: 'os\.Getenv'
        - pattern: 'os\.LookupEnv'
        - pattern: 'os\.Environ'
`

// writeGuardConfig writes a golangci-lint config that points forbidigo at
// env reads into dir and returns dir.
func writeGuardConfig(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), []byte(guardConfigYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestTypedAccessGuardUnconfigured pins when the lane is willing to run.
// The case that forced it is reliant's: a hand-owned .golangci.yml with no
// forbidigo settings, under which forbidigo checked its built-in fmt.Print
// patterns and the lane reported fmt.Println calls as typed-config findings
// while never looking at an os.Getenv.
func TestTypedAccessGuardUnconfigured(t *testing.T) {
	cases := []struct {
		name, file, body string
		wantReason       string // "" = configured (or not checkable here)
	}{
		{name: "no config at all", wantReason: "no golangci-lint config"},
		{name: "forge's scaffolded block", file: ".golangci.yml", body: guardConfigYAML},
		{
			name: "config without forbidigo settings", file: ".golangci.yml",
			body:       "version: \"2\"\nlinters:\n  default: standard\n  enable: [gosec]\n",
			wantReason: "no forbid patterns",
		},
		{
			name: "forbidigo forbids something else", file: ".golangci.yml",
			body:       "version: \"2\"\nlinters:\n  settings:\n    forbidigo:\n      forbid:\n        - pattern: '^fmt\\.Print.*$'\n",
			wantReason: "no pattern that forbids os.Getenv",
		},
		{
			name: "one pattern covering every env read", file: ".golangci.yaml",
			body: "linters:\n  settings:\n    forbidigo:\n      forbid:\n        - pattern: '^os\\.(Getenv|LookupEnv|Environ)$'\n",
		},
		{
			name: "v1 config, bare string entries", file: ".golangci.yml",
			body: "linters-settings:\n  forbidigo:\n    forbid:\n      - '^os\\.Getenv$'\n",
		},
		{
			name: "v1 config, p: entries", file: ".golangci.yml",
			body: "linters-settings:\n  forbidigo:\n    forbid:\n      - p: '^os\\.Getenv$'\n",
		},
		{
			name: "json config", file: ".golangci.json",
			body: `{"linters":{"settings":{"forbidigo":{"forbid":[{"pattern":"os\\.Getenv"}]}}}}`,
		},
		// Formats this check cannot read are left to golangci-lint, which
		// reports a config it cannot load itself.
		{name: "toml config", file: ".golangci.toml", body: "[linters]\n"},
		{name: "config that does not parse", file: ".golangci.yml", body: "linters: [\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := typedAccessGuardUnconfigured(dir)
			switch {
			case tc.wantReason == "" && got != "":
				t.Fatalf("want the lane to run, got reason %q", got)
			case tc.wantReason != "" && !strings.Contains(got, tc.wantReason):
				t.Fatalf("reason = %q, want it to contain %q", got, tc.wantReason)
			}
		})
	}
}

// TestFindGolangciConfigWalksUp: golangci-lint loads the nearest config at or
// above the directory it runs in, so a project nested in a repo whose config
// sits at the root is configured by that file.
func TestFindGolangciConfigWalksUp(t *testing.T) {
	root := writeGuardConfig(t, t.TempDir())
	nested := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := findGolangciConfig(nested), filepath.Join(root, ".golangci.yml"); got != want {
		t.Fatalf("findGolangciConfig = %q, want %q", got, want)
	}
	if reason := typedAccessGuardUnconfigured(nested); reason != "" {
		t.Fatalf("a parent's config should configure the lane; got %q", reason)
	}
}

// TestTypedAccessGuardFindingsAreAnchoredIssues is the "0 issues." defect: a
// clean forbidigo run printed one summary line, which became one warning, so
// `forge lint --scope` reported "typed-config guardrail — 1 warning" whose
// only detail was "0 issues.". A finding is a file-anchored issue; the
// summary and the source snippet under each issue are not.
func TestTypedAccessGuardFindingsAreAnchoredIssues(t *testing.T) {
	clean := anchoredFindings(externalLinesToFindings("0 issues.\n", ruleTypedAccessGuard, lintSevWarning))
	if len(clean) != 0 {
		t.Fatalf("a clean run produced findings: %+v", clean)
	}

	// Verbatim shape of a golangci-lint v2 report with two forbidigo issues.
	report := "internal/drain/drain.go:84:9: use of `os.Getenv` forbidden by pattern `os\\.Getenv` (forbidigo)\n" +
		"\traw := os.Getenv(name)\n" +
		"\t       ^\n" +
		"internal/x/y.go:12:3: use of `os.LookupEnv` forbidden by pattern `os\\.LookupEnv` (forbidigo)\n" +
		"\tv, ok := os.LookupEnv(\"X\")\n" +
		"\t         ^\n" +
		"2 issues:\n" +
		"* forbidigo: 2\n"
	fs := anchoredFindings(externalLinesToFindings(report, ruleTypedAccessGuard, lintSevWarning))
	if len(fs) != 2 {
		t.Fatalf("want the two anchored issues, got %d: %+v", len(fs), fs)
	}
	if fs[0].File != "internal/drain/drain.go" || fs[0].Line != 84 || fs[1].File != "internal/x/y.go" {
		t.Fatalf("issues lost their location: %+v", fs)
	}
}

// TestTypedAccessGuardCleanRunRendersNothing renders a clean guardrail run the
// way `forge lint --scope` does and checks that no warning line is printed.
func TestTypedAccessGuardCleanRunRendersNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake golangci-lint through a shell subprocess; runs in task test")
	}
	fakeGolangciLint(t, `echo "0 issues."; exit 0`)

	fs, gated, err := collectTypedAccessGuardJSON(testRunCtx(t, false))
	if err != nil || gated || len(fs) != 0 {
		t.Fatalf("clean run: want no findings, got gated=%v err=%v %+v", gated, err, fs)
	}
	var buf bytes.Buffer
	outcomes := []lintStepOutcome{
		{name: "golangci-lint", gates: true, ran: true}, // a verdict needs one gating lane
		{name: "typed-config guardrail", ran: true, findings: fs},
	}
	if err := renderLintOutcomes(&buf, outcomes, false, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "⚠") || strings.Contains(out, "0 issues") || !strings.Contains(out, "✓ typed-config guardrail") {
		t.Fatalf("a clean guardrail run must print only its ✓ line:\n%s", out)
	}
}

// TestTypedAccessGuardRefusesUnconfiguredConfig: with no env-read pattern
// configured, both drivers report the lane as could-not-run, name the fix,
// and never invoke golangci-lint — whose output would be forbidigo's default
// fmt.Print findings filed under the guardrail's name.
func TestTypedAccessGuardRefusesUnconfiguredConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake golangci-lint through a shell subprocess; runs in task test")
	}
	marker := filepath.Join(t.TempDir(), "invoked")
	fakeGolangciLint(t, `touch `+marker+`; echo "cmd/x/main.go:7:2: use of `+"`fmt.Println`"+` forbidden by pattern (forbidigo)"; exit 0`)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), []byte("version: \"2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := testRunCtx(t, false)
	rc.cwd = dir

	fs, gated, err := collectTypedAccessGuardJSON(rc)
	if err != nil || gated {
		t.Fatalf("an advisory lane must not gate without --strict: gated=%v err=%v", gated, err)
	}
	if len(fs) != 1 || fs[0].Rule != ruleTypedAccessGuardUnavailable || !strings.Contains(fs[0].Message, ".golangci.yml") {
		t.Fatalf("want one could-not-run finding naming the config file, got %+v", fs)
	}
	if !strings.Contains(fs[0].FixHint, `os\.Getenv`) {
		t.Errorf("the fix hint must name the patterns to add:\n%s", fs[0].FixHint)
	}

	// The lane is advisory, so its could-not-run is an advisory finding:
	// --strict is what makes it fail the run (lint_skip.go).
	rc.strict = true
	if _, gated, _ := collectGraded(t, findStep(t, "typed-config guardrail"), rc); !gated {
		t.Error("json: --strict must gate on a guardrail that could not run")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("golangci-lint was invoked against a config that does not configure the guardrail")
	}
}
