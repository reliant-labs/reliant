package lint

import (
	"os"
	"path/filepath"
	"testing"
)

// A go.work kept outside the tree and named by GOWORK is how a pinned project
// is bridged to an unreleased forge checkout without writing into it. The
// contract lane used to miss it (it looked only for a go.work file up the
// tree), added GOFLAGS=-mod=mod, and every package load then failed with
// "-mod may only be set to readonly or vendor when in workspace mode".
func TestHasWorkspaceGoMod_HonoursGOWORK(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "go.work")
	if err := os.WriteFile(outside, []byte("go 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inTree := t.TempDir()
	if err := os.WriteFile(filepath.Join(inTree, "go.work"), []byte("go 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noWork := t.TempDir()

	for _, tc := range []struct {
		name, gowork, cwd string
		want              bool
	}{
		{"GOWORK names an out-of-tree file", outside, noWork, true},
		{"GOWORK=off wins over an in-tree go.work", "off", inTree, false},
		{"unset: in-tree go.work", "", inTree, true},
		{"unset: no go.work anywhere", "", noWork, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOWORK", tc.gowork)
			t.Chdir(tc.cwd)
			if got := hasWorkspaceGoMod(); got != tc.want {
				t.Errorf("hasWorkspaceGoMod() = %v, want %v", got, tc.want)
			}
		})
	}
}
