package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/cigate"
)

// A path-gated extra check is settled by the branch's diff even when the
// unit-tests row is not narrowed (here CI_SKIP names it), and runs in full
// mode.
func TestApplyAffectedMode_GatesPathsWithoutNarrowingUnit(t *testing.T) {
	root := t.TempDir()
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeFile := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn("init", "-q", "-b", "main")
	writeFile("README.md", "x\n")
	gitIn("add", "-A")
	gitIn("commit", "-qm", "init")
	gitIn("update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn("checkout", "-qb", "feature")
	writeFile("docs/x.md", "y\n")

	checks := []cigate.Check{
		{Name: cigate.UnitTests, Argv: []string{"task", "test"}},
		{Name: "electron-tests", Paths: []string{"electron/**"}, Argv: []string{"npm", "test"}},
	}
	var out bytes.Buffer
	got := applyAffectedMode(context.Background(), &out, checks, affectedArgs{root: root, skips: []string{cigate.UnitTests}})
	if got[1].NotApplicable == "" || got[0].NotApplicable != "" || len(got[0].Argv) != 2 {
		t.Fatalf("unit %+v / electron %+v", got[0], got[1])
	}
	if out.Len() != 0 {
		t.Errorf("printed %q; the unit lane's mode line belongs to a run that narrows it", out.String())
	}

	got = applyAffectedMode(context.Background(), &out, checks, affectedArgs{root: root, skips: []string{cigate.UnitTests}, full: true})
	if got[1].NotApplicable != "" {
		t.Errorf("--full runs every path-gated check: %+v", got[1])
	}
}
