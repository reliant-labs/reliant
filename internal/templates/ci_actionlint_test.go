package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every rendered WORKFLOW passes actionlint: expression syntax, `needs:`
// references to jobs that exist, step-output references, job-level outputs,
// and shellcheck over every `run:`. YAML-parsing (the other template tests)
// proves GitHub can LOAD a file; actionlint proves the expressions inside it
// resolve — the class of bug a `{{ matrix.environment }}` missing its `$`
// belongs to.
//
// Skipped when actionlint is not on PATH: it is a developer and CI tool, not
// a build dependency. The composite action and dependabot.yml are not
// workflows, so actionlint does not take them.
func TestRenderedWorkflows_PassActionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not on PATH")
	}
	if testing.Short() {
		t.Skip("spawns actionlint (+ shellcheck) per workflow")
	}
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The vendored action must exist where release.yml `uses:` it, or
	// actionlint reports the local action as missing.
	actionDir := filepath.Join(dir, ".github", "actions", "forge-deploy")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for name, body := range renderedWorkflows(t) {
		switch name {
		case "dependabot":
			continue
		case "forge-deploy action":
			if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), body, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		p := filepath.Join(wfDir, strings.ReplaceAll(name, " ", "-")+".yml")
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	cmd := exec.Command(bin, append([]string{"-no-color"}, paths...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actionlint over the rendered workflows: %v\n%s", err, out)
	}
}
