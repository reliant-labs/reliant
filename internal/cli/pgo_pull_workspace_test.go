package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// The profile destination is checked after resolving symlinks: a sibling path
// whose parent is a symlink out of the workspace, or a destination that is
// itself such a link, is refused, because the write would land where it
// points.
func TestPGOWithinWorkspaceResolvesSymlinks(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	project := filepath.Join(ws, "app")
	sibling := filepath.Join(ws, "reliant")
	for _, d := range []string{project, filepath.Join(sibling, "cmd")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(sibling, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(sibling, "cmd", "default.pgo")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		target string
		want   bool
	}{
		{filepath.Join(project, "cmd", "api", "default.pgo"), true},
		{filepath.Join(sibling, "cmd", "reliant", "default.pgo"), true},
		{filepath.Join(sibling, "escape", "default.pgo"), false},
		{filepath.Join(sibling, "escape", "deeper", "default.pgo"), false},
		{filepath.Join(sibling, "cmd", "default.pgo"), false},
		{filepath.Join(outside, "default.pgo"), false},
	} {
		if got := pgoWithinWorkspace(project, c.target); got != c.want {
			t.Errorf("pgoWithinWorkspace(%s) = %v, want %v", c.target, got, c.want)
		}
	}
}
