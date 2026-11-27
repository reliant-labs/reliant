package cigate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// Reach modes of ci.gate.affected_reach.
const (
	ReachAll    = "all"
	ReachDirect = "direct"
)

// addDirectImports records what p imports for the package it belongs to: the
// package itself, its internal test variant ("a [a.test]") or its external
// test package ("a_test [a.test]"). A dependency recompiled for another
// package's tests ("a [b.test]") is not a's or b's direct import.
func (g *packageGraph) addDirectImports(path string, p listedPackage) {
	key := path
	if p.ForTest != "" {
		if path != p.ForTest && path != p.ForTest+"_test" {
			return
		}
		key = p.ForTest
	}
	for _, imp := range p.Imports {
		g.directImports[key] = append(g.directImports[key], stripTestVariant(imp))
	}
}

// applyReach narrows a.Packages under mode direct: the changed packages
// (a package whose own file, or a file it embeds, changed — selectChanges
// maps both to changedPkgs), those a rule names, and their direct importers
// (production or test imports) of either stay — a rule-selected package
// is a change target too, as reliant's test-only/db selections never defer. A package whose test binary links a
// change only through a deeper dependency is transitive by design. The rest — transitive
// importers — stay too when they number at most the cap, and are otherwise
// moved to a.Deferred for the full gate. This mirrors reliant's
// tools/test-affected `-reach direct -go-reach-cap N`: the budget is all or
// nothing, so a run never tests an arbitrary slice of the transitive set.
func applyReach(a *Affected, g *packageGraph, sel selection, reach *config.CIGateAffectedReach) {
	a.Reach = ReachAll
	if reach == nil || reach.Mode != ReachDirect {
		return
	}
	a.Reach, a.TransitiveCap = ReachDirect, reach.TransitiveCap
	var direct, transitive []string
	for _, p := range a.Packages {
		if sel.changedPkgs[p] || sel.rulePkgs[p] || slices.ContainsFunc(g.directImports[p], func(d string) bool { return sel.changedPkgs[d] || sel.rulePkgs[d] }) {
			direct = append(direct, p)
		} else {
			transitive = append(transitive, p)
		}
	}
	if len(transitive) <= reach.TransitiveCap {
		return
	}
	slices.Sort(transitive)
	a.Packages, a.Deferred = direct, transitive
}

// deferredLine is the clause Line adds when a direct reach deferred packages.
func (a Affected) deferredLine() string {
	if len(a.Deferred) == 0 {
		return ""
	}
	return fmt.Sprintf("; %d transitive importer(s) deferred to the full gate (over the %d-package cap): %s",
		len(a.Deferred), a.TransitiveCap, strings.Join(a.Deferred, ", "))
}
