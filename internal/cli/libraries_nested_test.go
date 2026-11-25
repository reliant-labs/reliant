package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Forge report d542c517: `forge project libraries observe/genai` failed "is
// not a forge/pkg package" and `libraries all` had no genai entry, though
// pkg/observe/genai exists. A package's own subpackages are listed, and
// selectable, by their path under pkg/; an internal/ tree is not.
func TestLibraries_NestedPackagesAreListedAndSelectable(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("observe/observe.go", "// Package observe is the parent.\npackage observe\n")
	write("observe/genai/genai.go", "// Package genai instruments model calls.\npackage genai\n\n// Tracer traces.\ntype Tracer struct{ Name string }\n\n// Start starts.\nfunc (t *Tracer) Start() {}\n")
	write("observe/genai/deeper/deeper.go", "// Package deeper is two levels down.\npackage deeper\n\nfunc Deep() {}\n")
	write("appkit/operatorkit/kit.go", "// Package operatorkit is under a grouping dir.\npackage operatorkit\n")
	write("fluxdelivery/fluxdelivery.go", "// Package fluxdelivery delivers.\npackage fluxdelivery\n")
	write("fluxdelivery/internal/kcleval/eval.go", "// Package kcleval is internal.\npackage kcleval\n")
	write("observe/genai/testdata/x.go", "package x\n")

	pkgs, err := readForgePkgPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range pkgs {
		names = append(names, p.Name)
		if p.ImportPath != forgePkgModule+"/"+p.Name {
			t.Errorf("%s: import path %q", p.Name, p.ImportPath)
		}
	}
	want := "appkit/operatorkit,fluxdelivery,observe,observe/genai,observe/genai/deeper"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("packages = %s, want %s", got, want)
	}

	spec := LibrariesSpec{Module: forgePkgModule, Dir: root, Packages: pkgs}
	if err := attachSignatures(&spec, "observe/genai.Tracer,"+forgePkgModule+"/observe/genai/deeper"); err != nil {
		t.Fatalf("selecting nested packages: %v", err)
	}
	got := map[string][]string{}
	for _, p := range spec.Packages {
		for _, s := range p.Symbols {
			got[p.Name] = append(got[p.Name], s.Name)
		}
	}
	if strings.Join(got["observe/genai"], ",") != "Tracer,Tracer.Start" {
		t.Errorf("observe/genai.Tracer selected %v, want the type and its method", got["observe/genai"])
	}
	if strings.Join(got["observe/genai/deeper"], ",") != "Deep" {
		t.Errorf("observe/genai/deeper selected %v", got["observe/genai/deeper"])
	}
	if len(got["observe"]) != 0 {
		t.Errorf("selecting a subpackage widened to its parent: %v", got["observe"])
	}
	if err := attachSignatures(&spec, "fluxdelivery/internal/kcleval"); err == nil {
		t.Error("an internal package was selectable")
	}
}

// The real tree: observe/genai is in the inventory.
func TestReadForgePkgPackages_ListsObserveGenai(t *testing.T) {
	t.Parallel()
	pkgs, err := readForgePkgPackages(repoPkgDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if p.Name == "observe/genai" {
			return
		}
	}
	t.Fatal("observe/genai is missing from the inventory")
}
