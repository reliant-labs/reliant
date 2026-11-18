package kcleval

// samedir_test.go pins the ProcessRooted check's comparison: one directory
// reached through a symlink is still the project root. On macOS /tmp is a
// symlink to /private/tmp and $TMPDIR lives under /var → /private/var, so a
// spelling comparison refused `forge project new` there (agents' bug reports,
// 2026-11-06..08).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameDir_ADirectoryReachedThroughASymlinkIsTheSameDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "private", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "tmp")
	if err := os.Symlink(filepath.Join(root, "private"), link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{"same spelling", project, project, true},
		{"unclean spelling", project, project + string(filepath.Separator) + ".", true},
		{"through a symlinked ancestor", project, filepath.Join(link, "project"), true},
		{"a different directory", project, other, false},
		{"a missing directory", project, filepath.Join(root, "missing"), false},
	} {
		if got := sameDir(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: sameDir(%s, %s) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}
