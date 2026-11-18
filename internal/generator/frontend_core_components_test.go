// Copyright (c) 2025 Reliant Labs
package generator

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/reliant-labs/forge/pkg/components"
)

// TestCoreComponentsResolve pins every scaffold-installed component to a real
// library entry. A name that does not resolve installs NOTHING and says
// nothing: installCoreComponents skips an empty body, so the frontend
// scaffolds without the file and the first failure is a missing import in a
// page the user did not write.
func TestCoreComponentsResolve(t *testing.T) {
	lib := components.NewLibrary()
	if len(coreComponents) == 0 {
		t.Fatal("coreComponents is empty — an empty set cannot fail this test")
	}
	for _, name := range coreComponents {
		if _, ok := lib.GetEntry(name); !ok {
			t.Errorf("coreComponents lists %q, which is not in the component library — "+
				"the scaffold would silently ship without it", name)
		}
	}
}

// TestCoreComponentsCoverTemplateImports pins every `@/components/ui/<name>`
// a frontend template imports to an installed component. forge has no
// import-driven install: a generated page that imports a component outside
// coreComponents (the create form's Textarea was one) fails `next build` /
// `vite build` in a project the user has not touched yet.
func TestCoreComponentsCoverTemplateImports(t *testing.T) {
	installed := map[string]bool{}
	for _, name := range coreComponents {
		installed[name] = true
	}
	importRe := regexp.MustCompile(`from "@/components/ui/([a-z0-9_]+)"`)
	root := filepath.Join("..", "templates", "frontend")
	seen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range importRe.FindAllStringSubmatch(string(body), -1) {
			seen++
			if !installed[m[1]] {
				t.Errorf("%s imports @/components/ui/%s, which the scaffold never installs — add it to coreComponents", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatalf("no @/components/ui/ import found under %s — the pattern no longer matches the templates", root)
	}
}

// TestCoreComponentsCoverMeasuredReimplementations pins the specific shapes a
// measured build hand-wrote while the library copies sat one call away.
//
// Three sub-agents fetched `empty_state` and all three wrote their own; another
// fetched `stat_grid` and hand-wrote a StatCard. The cause was cost, not
// ignorance — the library shipped hardcoded palette classes, so installing one
// meant re-theming every className. With the library themed, shipping these in
// the scaffold is what turns "fetch and reimplement" into "edit the file".
//
// This is a REGRESSION pin, not a wishlist: each name here was observed being
// reimplemented, so removing one should require observing that it stopped.
func TestCoreComponentsCoverMeasuredReimplementations(t *testing.T) {
	installed := map[string]bool{}
	for _, name := range coreComponents {
		installed[name] = true
	}
	for _, name := range []string{"empty_state", "stat_grid", "filter_bar", "confirmation_dialog"} {
		if !installed[name] {
			t.Errorf("%q was hand-reimplemented in a measured build and must ship in the scaffold", name)
		}
	}
}
