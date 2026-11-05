package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `forge generate` migrates a project scaffolded while the descriptor was
// committed: its .gitignore gains the rule once, and an ignored descriptor is
// left alone.
func TestEnsureDescriptorIgnored_AddsTheRuleOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	writeTreeFileT(t, filepath.Join(dir, ".gitignore"), "bin/")
	for range 2 {
		if err := ensureDescriptorIgnored(dir); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "bin/\n") || strings.Count(string(body), "/gen/forge_descriptor/") != 1 {
		t.Fatalf("want the user's rules kept and the descriptor rule added once:\n%s", body)
	}
	if err := exec.Command("git", "-C", dir, "check-ignore", "-q", "gen/forge_descriptor/services/a/v1/a.json").Run(); err != nil {
		t.Fatalf("the descriptor is not ignored after the migration: %v", err)
	}
}
