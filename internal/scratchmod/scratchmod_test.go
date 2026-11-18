package scratchmod

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
)

// The scratch module's build list is forge's: every requirement forge
// declares, at forge's version, plus forge itself replaced to the checkout,
// and forge's go.sum beside it. That is the whole offline guarantee, so it is
// pinned here without running the go command.
func TestWriteCopiesForgesBuildList(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := Write(root, dir, "example.com/scratch"); err != nil {
		t.Fatal(err)
	}
	forge := parse(t, filepath.Join(root, "go.mod"))
	got := parse(t, filepath.Join(dir, "go.mod"))

	if got.Module.Mod.Path != "example.com/scratch" || got.Go.Version != forge.Go.Version {
		t.Errorf("module %s go %s, want example.com/scratch go %s", got.Module.Mod.Path, got.Go.Version, forge.Go.Version)
	}
	required := map[string]string{}
	for _, r := range got.Require {
		required[r.Mod.Path] = r.Mod.Version
	}
	if _, ok := required[forge.Module.Mod.Path]; !ok {
		t.Errorf("the scratch module does not require %s", forge.Module.Mod.Path)
	}
	for _, r := range forge.Require {
		if v := required[r.Mod.Path]; v != r.Mod.Version {
			t.Errorf("requires %s at %q, want forge's %s", r.Mod.Path, v, r.Mod.Version)
		}
	}
	if len(got.Require) != len(forge.Require)+1 {
		t.Errorf("%d requirements, want forge's %d plus forge", len(got.Require), len(forge.Require))
	}
	replaced := false
	for _, r := range got.Replace {
		if r.Old.Path == forge.Module.Mod.Path {
			replaced = r.New.Path == root
		}
	}
	if !replaced {
		t.Errorf("forge is not replaced to the checkout %s: %+v", root, got.Replace)
	}

	want, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if sum, err := os.ReadFile(filepath.Join(dir, "go.sum")); err != nil || string(sum) != string(want) {
		t.Errorf("go.sum is not forge's (err %v)", err)
	}
}

func parse(t *testing.T, path string) *modfile.File {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
