package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

// stubStat is a map-backed statFunc. Keys are absolute paths the
// caller expects to "exist"; missing keys return an os.IsNotExist
// error. Lets unit tests assert the cwd-missing branch without
// poking the real filesystem.
type stubStat map[string]struct{}

func (s stubStat) Stat(path string) (os.FileInfo, error) {
	if _, ok := s[path]; ok {
		return nil, nil
	}
	// Use a real os.Stat against a nonexistent path to obtain a
	// PathError whose IsNotExist test fires.
	return os.Stat(filepath.Join(os.TempDir(), "_forge_doctor_test_nonexistent_"+filepath.Base(path)))
}

// stubLookupErr is a binaryLookupFunc that returns success for names
// in the set and error otherwise. Distinct from stubLookup (used in
// doctor_tools_test) because that one is a map[string]string keyed by
// path. We just care about presence here.
type stubLookupErr map[string]struct{}

func (s stubLookupErr) Lookup(name string) (string, error) {
	if _, ok := s[name]; ok {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

// TestBuildExternalBuildDoctorChecks_CwdPresentCmdOnPath is the happy
// path: build_cwd exists, first token resolves, → pass.
func TestBuildExternalBuildDoctorChecks_CwdPresentCmdOnPath(t *testing.T) {
	projectDir := "/proj"
	cwd := filepath.Join(projectDir, "sibling") // the host-separated path the check stats and reports
	stat := stubStat{cwd: {}}.Stat
	lookup := stubLookupErr{"docker": {}}.Lookup

	svcs := []WorkloadEntity{
		shellSvc("gw", "", "docker build .", "sibling", nil),
	}
	results := buildExternalBuildDoctorChecks(svcs, projectDir, lookup, stat)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Status != doctor.StatusPass {
		t.Errorf("status = %q, want pass; evidence=%q", r.Status, r.Evidence)
	}
	if !strings.Contains(r.Evidence, "ok: build_cwd "+cwd) {
		t.Errorf("evidence should report cwd present; got %s", r.Evidence)
	}
	if !strings.Contains(r.Evidence, "ok: docker on PATH") {
		t.Errorf("evidence should confirm docker on PATH; got %s", r.Evidence)
	}
	if !strings.Contains(r.Evidence, "info: build cmd (run verbatim): docker build .") {
		t.Errorf("evidence should show the verbatim cmd; got %s", r.Evidence)
	}
}

// TestBuildExternalBuildDoctorChecks_MissingCwdWarns — cwd not on
// disk → warn with a hint.
func TestBuildExternalBuildDoctorChecks_MissingCwdWarns(t *testing.T) {
	projectDir := "/proj"
	// Empty stat set → every path returns NotExist.
	stat := stubStat{}.Stat
	lookup := stubLookupErr{"docker": {}}.Lookup

	svcs := []WorkloadEntity{
		shellSvc("gw", "", "docker build .", "missing", nil),
	}
	results := buildExternalBuildDoctorChecks(svcs, projectDir, lookup, stat)
	r := results[0]
	if r.Status != doctor.StatusWarn {
		t.Fatalf("status = %q, want warn", r.Status)
	}
	if !strings.Contains(r.Evidence, "warn: build_cwd "+filepath.Join(projectDir, "missing")+" does not exist on disk") {
		t.Errorf("evidence should call out missing cwd; got %s", r.Evidence)
	}
	if !strings.Contains(r.Evidence, "skip-with-warn") {
		t.Errorf("evidence should mention skip-with-warn semantics; got %s", r.Evidence)
	}
}

// TestBuildExternalBuildDoctorChecks_FirstTokenMissingWarns — `go`
// not on PATH → warn with the verbatim command still visible (the
// preview line is always emitted regardless of warn state).
func TestBuildExternalBuildDoctorChecks_FirstTokenMissingWarns(t *testing.T) {
	projectDir := "/proj"
	stat := stubStat{projectDir: {}}.Stat // cwd ok (empty BuildCwd = projectDir)
	lookup := stubLookupErr{}.Lookup      // nothing on PATH

	svcs := []WorkloadEntity{
		shellSvc("gw", "", "go build ./...", "", nil),
	}
	results := buildExternalBuildDoctorChecks(svcs, projectDir, lookup, stat)
	r := results[0]
	if r.Status != doctor.StatusWarn {
		t.Fatalf("status = %q, want warn", r.Status)
	}
	if !strings.Contains(r.Evidence, "warn: go not found on PATH") {
		t.Errorf("evidence should call out missing first token; got %s", r.Evidence)
	}
	if !strings.Contains(r.Evidence, "info: build cmd (run verbatim): go build ./...") {
		t.Errorf("evidence should still show the verbatim cmd; got %s", r.Evidence)
	}
}

// TestBuildExternalBuildDoctorChecks_FirstTokenSkippedWhenCdPrefix —
// `cd` prefix skips the PATH heuristic (otherwise we'd always pass
// because `cd` is a shell builtin everywhere) but the info-preview
// stays.
func TestBuildExternalBuildDoctorChecks_FirstTokenSkippedWhenCdPrefix(t *testing.T) {
	projectDir := "/proj"
	stat := stubStat{projectDir: {}}.Stat
	lookup := stubLookupErr{}.Lookup // even with empty PATH, we should not warn

	svcs := []WorkloadEntity{
		shellSvc("gw", "", "cd ../sibling && docker build .", "", nil),
	}
	results := buildExternalBuildDoctorChecks(svcs, projectDir, lookup, stat)
	r := results[0]
	if r.Status != doctor.StatusPass {
		t.Errorf("status = %q, want pass (cd-prefix skips heuristic); evidence=%q", r.Status, r.Evidence)
	}
	if !strings.Contains(r.Evidence, "first-token PATH check skipped") {
		t.Errorf("evidence should explain skipped heuristic; got %s", r.Evidence)
	}
	if !strings.Contains(r.Evidence, "info: build cmd (run verbatim): cd ../sibling && docker build .") {
		t.Errorf("evidence should still show the verbatim cmd; got %s", r.Evidence)
	}
}

// TestBuildExternalBuildDoctorChecks_FirstTokenSkippedWhenEnvVar —
// `KEY=value` first token (e.g. `CGO_ENABLED=0 go build`) skips the
// heuristic for the same reason as cd.
func TestBuildExternalBuildDoctorChecks_FirstTokenSkippedWhenEnvVar(t *testing.T) {
	projectDir := "/proj"
	stat := stubStat{projectDir: {}}.Stat
	lookup := stubLookupErr{}.Lookup

	svcs := []WorkloadEntity{
		shellSvc("gw", "", "CGO_ENABLED=0 go build ./...", "", nil),
	}
	results := buildExternalBuildDoctorChecks(svcs, projectDir, lookup, stat)
	r := results[0]
	if r.Status != doctor.StatusPass {
		t.Errorf("status = %q, want pass (env-var prefix skips heuristic); evidence=%q", r.Status, r.Evidence)
	}
	if !strings.Contains(r.Evidence, "first-token PATH check skipped") {
		t.Errorf("evidence should mention skipped heuristic; got %s", r.Evidence)
	}
}

// Doctor reports the command VERBATIM — the exact string forge will hand
// `sh -c`, with no placeholders standing in for anything.
//
// It used to render a preview against a synthetic Spec of stand-in values
// (<registry>, <tag>, <arch>), which was the best it could do while a
// substitution pass sat between the declaration and the shell. That preview was
// a fiction nobody could act on: it showed neither what the KCL declared nor
// what would actually run. Now the two are the same string, so doctor shows it.
func TestBuildExternalBuildDoctorChecks_ReportsCmdVerbatim(t *testing.T) {
	projectDir := "/proj"
	stat := stubStat{projectDir: {}}.Stat
	lookup := stubLookupErr{"docker": {}}.Lookup

	// A rendered cmd: KCL already resolved the reference and the arch, and a
	// ${HOME} the SHELL owns survives into it.
	cmd := `docker build -t reg.example.com/my-gw:v1.2.3 --platform=linux/arm64 --build-arg H=${HOME} .`
	svcs := []WorkloadEntity{shellSvc("gw", "my-gw", cmd, "", nil)}
	results := buildExternalBuildDoctorChecks(svcs, projectDir, lookup, stat)
	r := results[0]
	if want := "info: build cmd (run verbatim): " + cmd; !strings.Contains(r.Evidence, want) {
		t.Errorf("evidence missing the verbatim cmd\n  want substring: %s\n  got: %s", want, r.Evidence)
	}
	// No placeholder vocabulary may survive: a stand-in would imply forge
	// still resolves something at exec time.
	for _, gone := range []string{"<registry>", "<tag>", "<arch>", "resolved build cmd"} {
		if strings.Contains(r.Evidence, gone) {
			t.Errorf("evidence still carries %q from the deleted substituted preview: %s", gone, r.Evidence)
		}
	}
}

// TestRunExternalBuildDoctorChecks_FeatureOffReturnsNil — when
// the build feature is off, the wrapper returns no checks. Mirrors
// runIngressDoctorChecks's feature-off behaviour.
func TestRunExternalBuildDoctorChecks_FeatureOffReturnsNil(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name:     "t",
		Features: config.FeaturesConfig{}.With(config.FeatureBuild, false),
	}
	results := runExternalBuildDoctorChecks(context.Background(), cfg, t.TempDir(), "")
	if results != nil {
		t.Errorf("want nil; got %d results", len(results))
	}
}

// TestRunExternalBuildDoctorChecks_SignalFilter — non-matching
// signal also suppresses the checks.
func TestRunExternalBuildDoctorChecks_SignalFilter(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "t"}
	results := runExternalBuildDoctorChecks(context.Background(), cfg, t.TempDir(), "metrics")
	if results != nil {
		t.Errorf("want nil for signal=metrics; got %d", len(results))
	}
}

// TestRunExternalBuildDoctorChecks_NoServicesReturnsNil — project
// with KCL but no build_cmd services emits zero checks (no noise).
func TestRunExternalBuildDoctorChecks_NoServicesReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy", "kcl", "dev", "main.k"), []byte("// stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "image": "r/api",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`))
	cfg := &config.ProjectConfig{Name: "t"}
	results := runExternalBuildDoctorChecks(context.Background(), cfg, dir, "")
	if results != nil {
		t.Errorf("want nil when no build_cmd services declared; got %d", len(results))
	}
}

// TestRunExternalBuildDoctorChecks_KCLFailureSurfacedAsSkip — when
// the dev KCL can't render (no deploy/kcl/dev dir), the wrapper
// emits one skipped check rather than failing the whole report.
// Mirrors runIngressDoctorChecks's KCL-failure path.
func TestRunExternalBuildDoctorChecks_KCLFailureSurfacedAsSkip(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "t"}
	results := runExternalBuildDoctorChecks(context.Background(), cfg, t.TempDir(), "")
	if len(results) != 1 {
		t.Fatalf("want 1 result (skipped); got %d", len(results))
	}
	if results[0].Status != doctor.StatusSkip {
		t.Errorf("status = %q, want skip", results[0].Status)
	}
}

// TestFirstCommandToken covers the pure helper.
func TestFirstCommandToken(t *testing.T) {
	cases := []struct {
		name       string
		cmd        string
		wantToken  string
		wantReason string
	}{
		{"empty", "", "", ""},
		{"whitespace", "   ", "", ""},
		{"simple", "docker build .", "docker", ""},
		{"go", "go build ./...", "go", ""},
		{"cd_prefix", "cd ../sibling && docker build .", "", "build_cmd starts with `cd` — first-token heuristic doesn't apply"},
		{"cd_capitalized", "CD foo && bar", "", "build_cmd starts with `cd` — first-token heuristic doesn't apply"},
		{"env_var", "CGO_ENABLED=0 go build", "", "build_cmd starts with env-var assignment — first-token heuristic doesn't apply"},
		{"env_var_lower_passes_through", "key=value go build", "key=value", ""}, // not uppercase → not detected as env, returned as-is
		{"tabs_split", "docker\tbuild .", "docker", ""},
		// Shell builtins are never on PATH, so a lookup always "fails".
		// `set -e` opens most multi-step build_cmds, and warning about it
		// told two real targets (workspace-base, daemon-gateway) their
		// build was broken for doing the careful thing.
		{"set_e", "set -e\ndocker build .", "", "build_cmd starts with the shell builtin `set` — first-token heuristic doesn't apply"},
		{"set_euo", "set -euo pipefail\ngo build ./...", "", "build_cmd starts with the shell builtin `set` — first-token heuristic doesn't apply"},
		{"export", "export FOO=1\ngo build", "", "build_cmd starts with the shell builtin `export` — first-token heuristic doesn't apply"},
		// A word that is BOTH a builtin and a real binary is not skipped —
		// looking it up is harmless and still informative.
		{"echo_not_skipped", "echo hi", "echo", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			token, reason := firstCommandToken(c.cmd)
			if token != c.wantToken {
				t.Errorf("token = %q, want %q", token, c.wantToken)
			}
			if reason != c.wantReason {
				t.Errorf("reason = %q, want %q", reason, c.wantReason)
			}
		})
	}
}

// TestIsShellEnvKey covers the env-key heuristic edges.
func TestIsShellEnvKey(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"", false},
		{"CGO_ENABLED", true},
		{"PATH", true},
		{"_INTERNAL", true},
		{"GO111MODULE", true},
		{"lowercase", false},
		{"Mixed_Case", false},
		{"1FOO", false},
		{"FOO-BAR", false},
	}
	for _, c := range cases {
		if got := isShellEnvKey(c.s); got != c.want {
			t.Errorf("isShellEnvKey(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

// TestAppendExternalBuildChecksToReport_WarnEscalates confirms the
// rollup: a warn-level external-build check pushes report.Overall
// from Pass → Warn (matching the standard rule).
func TestAppendExternalBuildChecksToReport_WarnEscalates(t *testing.T) {
	report := &doctor.Report{Overall: doctor.StatusPass}
	appendExternalBuildChecksToReport(report, []doctor.CheckResult{
		{Name: "external-build: gw", Status: doctor.StatusWarn, Duration: 1 * time.Millisecond},
	})
	if report.Overall != doctor.StatusWarn {
		t.Errorf("overall = %q, want warn", report.Overall)
	}
	if len(report.Checks) != 1 {
		t.Errorf("want 1 check appended; got %d", len(report.Checks))
	}
}
