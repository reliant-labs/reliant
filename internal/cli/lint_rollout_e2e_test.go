//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2ENewRuleWarnsUntilPinReachesGatesFrom drives the real binary over the
// rollout primitive (internal/cli/lint/lint_rollout.go): a rule tightened in a
// release the project's forge_version has not been through is a warning, and
// the verdict line — the last line of the run — says so; pinning the release
// it gates from turns the same finding into a failure; lint.rules opts in
// early. forge-column-markers is a rule that is warn-first at this commit.
func TestE2ENewRuleWarnsUntilPinReachesGatesFrom(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "rollapp", "--mod", "example.com/rollapp", "--kind", "cli")
	projectDir := filepath.Join(dir, "rollapp")

	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "20260101000000_bad_marker.up.sql"),
		[]byte("COMMENT ON COLUMN invoices.total_cents IS 'forge:immutible';\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pin := func(version string) {
		t.Helper()
		path := filepath.Join(projectDir, "forge.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`(?m)^forge_version:.*$`)
		if !re.Match(raw) {
			t.Fatalf("forge.yaml has no forge_version line:\n%s", raw)
		}
		if err := os.WriteFile(path, re.ReplaceAll(raw, []byte("forge_version: "+version)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lint := func() (string, error) {
		cmd := exec.Command(forgeBin, "lint", "--column-markers", "--no-fix")
		cmd.Dir = projectDir
		// The pin names a release this tree-built binary is not; do not let
		// the skew guard hand the run to a downloaded forge.
		cmd.Env = append(os.Environ(), "FORGE_ALLOW_VERSION_SKEW=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	lastLine := func(s string) string {
		lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
		return lines[len(lines)-1]
	}

	pin("v0.1.46")
	out, err := lint()
	if err != nil {
		t.Fatalf("a project pinned below gates_from must pass with a warning, got %v:\n%s", err, out)
	}
	for _, want := range []string{"is new in v1.0.0 and gates from v1.0.1", "lint.rules: {forge-column-markers: error}"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
	last := lastLine(out)
	for _, want := range []string{"✅", "1 new-rule finding warn until they gate", "forge-column-markers ×1, gates from v1.0.1"} {
		if !strings.Contains(last, want) {
			t.Errorf("verdict line %q does not say %q", last, want)
		}
	}

	pin("v1.0.1")
	if out, err := lint(); err == nil {
		t.Fatalf("a project pinned at gates_from must fail on the same finding:\n%s", out)
	} else if strings.Contains(out, "is new in v1.0.0") {
		t.Errorf("a gating finding still carries the grace note:\n%s", out)
	}

	pin("v0.1.46")
	cfgPath := filepath.Join(projectDir, "forge.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(raw, []byte("lint:\n  rules:\n    forge-column-markers: error\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := lint(); err == nil {
		t.Fatalf("lint.rules forge-column-markers: error must gate a project still in the grace period:\n%s", out)
	}
}
