package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// The two pre-commit files forge scaffolds are themselves checked by the
// prettier hook they configure (files: \.(…|yml|yaml|…)$), so each must
// already be in prettier's shape or a fresh project's first commit fails.
// houndersclub hit exactly this adopting v0.1.18: pre-commit.yml's
// `python-version: '3.12'` and the commitlint hook's
// `['@commitlint/config-conventional']` were both rewritten.
//
// The Go-side guard (no single-quoted scalar — prettier's YAML printer
// rewrites every one to double quotes) runs everywhere; the real prettier
// run needs node and is skipped in -short mode.
func TestPreCommitFiles_PrettierClean(t *testing.T) {
	g := &ProjectGenerator{Name: "demo", Path: t.TempDir()}
	if err := g.generatePreCommitConfig(); err != nil {
		t.Fatal(err)
	}
	if err := g.generatePreCommitWorkflow(); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(g.Path, ".pre-commit-config.yaml"),
		filepath.Join(g.Path, ".github", "workflows", "pre-commit.yml"),
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var root yaml.Node
		if err := yaml.Unmarshal(raw, &root); err != nil {
			t.Fatalf("%s is not valid YAML: %v", p, err)
		}
		var quoted []string
		var walk func(n *yaml.Node)
		walk = func(n *yaml.Node) {
			if n.Kind == yaml.ScalarNode && n.Style&yaml.SingleQuotedStyle != 0 {
				quoted = append(quoted, fmt.Sprintf("line %d: '%s'", n.Line, n.Value))
			}
			for _, c := range n.Content {
				walk(c)
			}
		}
		walk(&root)
		if len(quoted) > 0 {
			t.Errorf("%s: single-quoted scalars prettier rewrites to double quotes:\n  %s", filepath.Base(p), strings.Join(quoted, "\n  "))
		}
	}

	if testing.Short() {
		t.Skip("-short: skipping the prettier run (downloads prettier on first use); the single-quote guard above still ran")
	}
	npx := requirePrettierRunner(t)
	// The version the scaffolded hook pins.
	v := templates.PrettierVersion
	args := append([]string{"-y", "prettier@" + v, "--no-config", "--check"}, paths...)
	if out, err := exec.Command(npx, args...).CombinedOutput(); err != nil {
		t.Fatalf("prettier %s would rewrite the scaffolded pre-commit files, so a fresh project fails its own hook (%v):\n%s", v, err, out)
	}
}

// requirePrettierRunner returns npx, or skips when node is absent on a
// developer machine — and FAILS under CI (or FORGE_E2E_REQUIRE_TOOLS=1),
// where a missing node is a provisioning bug, not a reason to report green
// for a check that never ran.
func requirePrettierRunner(t *testing.T) string {
	t.Helper()
	npx, lookErr := exec.LookPath("npx")
	// Shaped like internal/cli's requireTool: the skip is the guarded branch
	// and the failure the fall-through, so vacuousguard (and a reader) sees
	// exactly what the skip depends on.
	if lookErr != nil && os.Getenv("CI") == "" && os.Getenv("FORGE_E2E_REQUIRE_TOOLS") == "" {
		t.Skip("npx (node) not on PATH: cannot run prettier — skipped locally, a hard failure under CI; the single-quote guard above still ran")
	}
	if lookErr != nil {
		t.Fatalf("npx (node) not on PATH under CI: install node in the job so the prettier check actually runs")
	}
	return npx
}
