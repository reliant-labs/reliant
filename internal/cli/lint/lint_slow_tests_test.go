package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/slowtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The lane end to end on a real module: the analyzer's finding comes back
// with a module-relative file:line, and a `forge:lint-disable-next-line`
// directive with a reason silences it like any other forge lint rule.
func TestSlowTestsLaneReportsAndHonoursSuppression(t *testing.T) {
	testkit.Slow(t, "loads a scratch module through `go list`")
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("m.go", "package m\n")
	write("m_test.go", `package m

import (
	"os/exec"
	"testing"
)

func TestBuilds(t *testing.T) {
	_ = exec.Command("go", "build", "./...").Run()
}

// forge:lint-disable-next-line forge-slow-test-unmarked: measured at 40ms; the repo is t.TempDir
func TestInitsRepo(t *testing.T) {
	_ = exec.Command("git", "init", t.TempDir()).Run()
}
`)
	t.Chdir(dir)

	findings, err := runSlowTestAnalysis(context.Background(), []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1 (TestInitsRepo is suppressed): %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Rule != slowtest.RuleUnmarked || f.File != "m_test.go" || f.Line != 8 || !strings.Contains(f.Message, "TestBuilds") {
		t.Errorf("finding = %+v, want %s at m_test.go:8 naming TestBuilds", f, slowtest.RuleUnmarked)
	}
	if !strings.Contains(f.Remediation, "testkit.Slow") {
		t.Errorf("remediation does not say how to mark the test: %q", f.Remediation)
	}
}
