package connectmajor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// V2ScaffoldVersion is the connect-go v2 release forge itself requires: the
// scaffolded go.mod and gen/go.mod require it, and a --buf-plugins=remote v2
// project pins the BSR plugin at it, so a forge bump of connect-go v2 that
// forgets the scaffold fails here instead of shipping a project whose
// plugin and runtime disagree.
func TestV2ScaffoldVersionMatchesForgeGoMod(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.ParseLax(path, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	var forge string
	for _, r := range f.Require {
		if r.Mod.Path == ModuleV2 {
			forge = r.Mod.Version
		}
	}
	if forge != V2ScaffoldVersion {
		t.Fatalf("forge's go.mod requires %s %s, but V2ScaffoldVersion is %s", ModuleV2, forge, V2ScaffoldVersion)
	}
	for _, tmpl := range []string{"go.mod.tmpl", "gen-go.mod.tmpl"} {
		body, err := os.ReadFile(filepath.Join(root, "internal", "templates", "project", tmpl))
		if err != nil {
			t.Fatal(err)
		}
		if want := ModuleV2 + " " + V2ScaffoldVersion + "\n"; !strings.Contains(string(body), want) {
			t.Errorf("internal/templates/project/%s does not require %q", tmpl, strings.TrimSpace(want))
		}
	}
}
