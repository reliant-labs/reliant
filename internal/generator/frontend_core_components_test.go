// Copyright (c) 2025 Reliant Labs
package generator

import (
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
