package cli

// kcl_eval_test.go covers the CLI surface of `forge kcl eval`: the flags, the
// stdout/stderr split, and the exit behaviour. The evaluation itself is covered
// in internal/kcleval.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/kclvendor"
)

// evalProject writes a minimal project and returns its root.
func evalProject(t *testing.T) string {
	t.Helper()
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"forge.yaml":         "name: evalfixture\nmodule: example.com/evalfixture\n",
		"deploy/kcl/kcl.mod": "[package]\nname = \"evalfixture\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/lib/pool.k": `import forge

sbd = {
    image_family = "workspace-sbd"
    disk_size_gb = 200
}
tiers = ["small", "large"]
_p = forge.Bundle is not None
`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runEval invokes the command in dir and returns stdout, stderr and the error.
func runEval(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	root := &cobra.Command{Use: "forge", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(newKCLCmd())
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"kcl", "eval"}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

// TestKCLEvalRawIsExactlyTheValue is the contract a shell caller depends on:
// `V="$(forge kcl eval … --format raw)"` captures the value and nothing else.
//
// STDOUT carrying one stray line of prose is the failure this pins. `env
// render` shipped that bug — a `Note:` on stdout became the first YAML document
// of a stream piped to kubectl — and here it would silently become part of V.
func TestKCLEvalRawIsExactlyTheValue(t *testing.T) {
	dir := evalProject(t)

	stdout, _, err := runEval(t, dir, "deploy/kcl/lib/pool.k", "-S", "sbd.image_family", "--format", "raw")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "workspace-sbd" {
		t.Errorf("stdout = %q, want exactly %q (no quotes, no newline)", stdout, "workspace-sbd")
	}

	// An int keeps its integer form, which a caller passing it to a size flag
	// depends on.
	stdout, _, err = runEval(t, dir, "deploy/kcl/lib/pool.k", "-S", "sbd.disk_size_gb", "--format", "raw")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "200" {
		t.Errorf("stdout = %q, want %q", stdout, "200")
	}
}

// TestKCLEvalRepeatedSelect: two -S flags yield an object keyed by selector.
func TestKCLEvalRepeatedSelect(t *testing.T) {
	dir := evalProject(t)
	stdout, _, err := runEval(t, dir, "deploy/kcl/lib/pool.k",
		"-S", "sbd.image_family", "-S", "sbd.disk_size_gb")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"sbd.image_family": "workspace-sbd"`) ||
		!strings.Contains(stdout, `"sbd.disk_size_gb": 200`) {
		t.Errorf("repeated -S stdout:\n%s", stdout)
	}
}

// TestKCLEvalRawRefusesAComposite: raw of a list fails rather than printing
// JSON the caller would interpolate into a command line without noticing.
func TestKCLEvalRawRefusesAComposite(t *testing.T) {
	dir := evalProject(t)
	stdout, _, err := runEval(t, dir, "deploy/kcl/lib/pool.k", "-S", "tiers", "--format", "raw")
	if err == nil {
		t.Fatalf("--format raw of a list succeeded, printing %q", stdout)
	}
	if stdout != "" {
		t.Errorf("a refused evaluation wrote to stdout: %q", stdout)
	}
}

// TestKCLEvalWalksUpToTheProject: run from a subdirectory with no -C, the
// command still finds the project. A Go test runs in its package directory and
// a script runs from wherever it was invoked, so this is the common case.
func TestKCLEvalWalksUpToTheProject(t *testing.T) {
	dir := evalProject(t)
	sub := filepath.Join(dir, "internal", "operators", "shared")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runEval(t, sub, "deploy/kcl/lib/pool.k", "-S", "sbd.image_family", "--format", "raw")
	if err != nil {
		t.Fatalf("eval from a subdirectory: %v", err)
	}
	if stdout != "workspace-sbd" {
		t.Errorf("stdout = %q", stdout)
	}
}

// TestKCLEvalMissingFieldFailsWithoutAUsageDump: a selector typo is the user's
// mistake, and cobra's usage block would bury the message naming the fields the
// document actually has.
func TestKCLEvalMissingFieldFailsWithoutAUsageDump(t *testing.T) {
	dir := evalProject(t)
	_, stderr, err := runEval(t, dir, "deploy/kcl/lib/pool.k", "-S", "sbd.image_familly")
	if err == nil {
		t.Fatal("a typo'd selector succeeded")
	}
	if !strings.Contains(err.Error(), "image_family") {
		t.Errorf("error does not name the available field:\n%v", err)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Errorf("a selector typo printed a usage dump:\n%s", stderr)
	}
}

// TestKCLEvalOptionReachesTheFile: -D binds a top-level option().
func TestKCLEvalOptionReachesTheFile(t *testing.T) {
	dir := evalProject(t)
	p := filepath.Join(dir, "deploy", "kcl", "lib", "opt.k")
	body := "import forge\n\ntier = option(\"tier\", type=\"str\", default=\"free\")\n_p = forge.Bundle is not None\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runEval(t, dir, "deploy/kcl/lib/opt.k", "-S", "tier", "--format", "raw", "-D", "tier=paid")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "paid" {
		t.Errorf("-D tier=paid gave %q, want \"paid\"", stdout)
	}
}

// TestKCLEvalClaimsNoPorts: a read-only command never claims a port block.
func TestKCLEvalClaimsNoPorts(t *testing.T) {
	dir := evalProject(t)
	if _, _, err := runEval(t, dir, "deploy/kcl/lib/pool.k", "-S", "sbd.image_family"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".forge/blocks.json", ".forge/ports-dev.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("eval created %s; it must claim nothing", rel)
		}
	}
}
