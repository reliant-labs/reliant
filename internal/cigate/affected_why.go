package cigate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// Why-reporting and the build set of affected mode.
//
// A narrowed unit lane is only trusted when it can say why each package runs,
// so Affected carries three explanations beside the package list:
//
//   - Why: per selected test package, the first changed file that reached it
//     and the edge it came by — the file is in the package, the package's test
//     binary imports the changed package, or an affected rule named it;
//   - Files: per changed file, the package it changes, the affected rules that
//     claimed it, and whether affected_ignore lists it;
//   - Build: every module package (tests or not) whose build contains a
//     changed package, for a compile phase that must not stop at the packages
//     with tests.

// PackageWhy says why one test package was selected.
type PackageWhy struct {
	Package string `json:"package"`
	// File is the first changed file (sorted) that selected it.
	File string `json:"file"`
	// Edge is how File reached Package: "changed" (File is part of it),
	// "imports <pkg>" (its test binary links the changed package <pkg>), or
	// "ci.gate.affected_rules[<i>]".
	Edge string `json:"edge"`
}

// FileReport is what one changed file maps to.
type FileReport struct {
	Path string `json:"path"`
	// Package is the Go package the file changes ("" when none: not Go
	// source, an embedded file or a package's testdata).
	Package string `json:"package,omitempty"`
	// Rules are the indexes of the ci.gate.affected_rules that claimed it.
	Rules []int `json:"rules,omitempty"`
	// Ignored says ci.gate.affected_ignore lists it.
	Ignored bool `json:"ignored,omitempty"`
	// Note is a one-line summary of the above, as the gate prints it.
	Note string `json:"note"`
}

// explainFiles maps each changed file to its package, claiming rules and
// ignore status. It never decides the mode; selectChanges does.
func explainFiles(changed []string, g *packageGraph, ignore []string, rules []ruleMatcher) []FileReport {
	out := make([]FileReport, 0, len(changed))
	for _, f := range changed {
		r := FileReport{Path: f, Ignored: matchGlobs(f, ignore)}
		for i, m := range rules {
			if m.match(f) {
				r.Rules = append(r.Rules, i)
			}
		}
		if !r.Ignored {
			if pkg, ok := g.owner(f); ok {
				r.Package = pkg
			}
		}
		var parts []string
		if r.Package != "" {
			parts = append(parts, "package "+r.Package)
		}
		for _, i := range r.Rules {
			parts = append(parts, fmt.Sprintf("ci.gate.affected_rules[%d]", i))
		}
		if r.Ignored {
			parts = append(parts, "ci.gate.affected_ignore")
		}
		if len(parts) == 0 {
			parts = append(parts, "unclaimed — runs the full suite")
		}
		r.Note = strings.Join(parts, "; ")
		out = append(out, r)
	}
	return out
}

// ruleMatcher is the part of an affected rule explainFiles needs.
type ruleMatcher struct {
	paths, except, packages []string
}

func (m ruleMatcher) match(f string) bool {
	return matchGlobs(f, m.paths) && !matchGlobs(f, m.except)
}

// explainPackages gives each selected test package its first reason.
func explainPackages(pkgs []string, files []FileReport, g *packageGraph, rules []ruleMatcher) []PackageWhy {
	// The first changed file of each changed package (files are sorted).
	firstFile := map[string]string{}
	for _, f := range files {
		if f.Package != "" {
			if _, ok := firstFile[f.Package]; !ok {
				firstFile[f.Package] = f.Path
			}
		}
	}
	out := make([]PackageWhy, 0, len(pkgs))
	for _, pkg := range pkgs {
		why := PackageWhy{Package: pkg}
		if f, ok := firstFile[pkg]; ok {
			why.File, why.Edge = f, "changed"
		} else {
			// The dependency whose first changed file sorts first.
			for _, d := range g.testDeps[pkg] {
				if f, ok := firstFile[d]; ok && (why.File == "" || f < why.File) {
					why.File, why.Edge = f, "imports "+d
				}
			}
		}
		if why.File == "" {
		rule:
			for _, f := range files {
				for _, i := range f.Rules {
					for _, pat := range rules[i].packages {
						for _, p := range g.resolve(pat) {
							if p == pkg {
								why.File, why.Edge = f.Path, fmt.Sprintf("ci.gate.affected_rules[%d]", i)
								break rule
							}
						}
					}
				}
			}
		}
		out = append(out, why)
	}
	return out
}

// buildSet is every module package whose build — itself and what it imports,
// tests excluded — contains a changed package. Sorted.
func (g *packageGraph) buildSet(changed map[string]bool) []string {
	var out []string
	for pkg, deps := range g.buildDeps {
		if changed[pkg] {
			out = append(out, pkg)
			continue
		}
		for _, d := range deps {
			if changed[d] {
				out = append(out, pkg)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// WhyLines are the per-package lines the gate prints under the mode line.
func (a Affected) WhyLines() []string {
	out := make([]string, 0, len(a.Why))
	for _, w := range a.Why {
		out = append(out, fmt.Sprintf("%s ← %s (%s)", w.Package, w.File, w.Edge))
	}
	return out
}

func ruleMatchers(rules []config.CIGateAffectedRule) []ruleMatcher {
	out := make([]ruleMatcher, len(rules))
	for i, r := range rules {
		out[i] = ruleMatcher{paths: r.Paths, except: r.Except, packages: r.Packages}
	}
	return out
}

// computeAffectedFiles judges in.Files as the changed set, without git: the
// what-if of `forge ci gate --plan --files`.
func computeAffectedFiles(ctx context.Context, in AffectedInput) Affected {
	changed := uniqueLines(strings.Join(in.Files, "\n"))
	a := Affected{BaseRef: "--files", Base: "what-if", Changed: changed}
	if in.ChangesOnly {
		return a
	}
	return affectedFromGraph(ctx, in, a)
}

// DocumentAffected is the affected-mode decision in the forge.ci.gate/v1
// document (`affected`), and the whole output of `forge ci gate --plan --json`.
type DocumentAffected struct {
	Full   bool   `json:"full"`
	Reason string `json:"reason,omitempty"`
	// BaseRef and Base are the ref diffed against and its merge-base
	// ("--files" / "what-if" for a what-if).
	BaseRef string `json:"base_ref,omitempty"`
	Base    string `json:"base,omitempty"`
	// Total is how many module packages have tests.
	Total     int          `json:"total"`
	Changed   []string     `json:"changed"`
	Packages  []string     `json:"packages"`
	Build     []string     `json:"build"`
	Frontends []string     `json:"frontends"`
	Why       []PackageWhy `json:"why"`
	Files     []FileReport `json:"files"`
	// Selections are the per-rule lines the gate prints.
	Selections []string `json:"selections"`
	// Line is the mode line the gate prints.
	Line string `json:"line"`
	// Reach is ci.gate.affected_reach's mode ("all" or "direct"), with the
	// direct mode's TransitiveCap. Deferred are the test packages a direct
	// reach left out (transitive importers past the cap); always a list.
	// FullGateRequired is true when Deferred is non-empty: this run did not
	// test them, and the full gate (main tier, CI_FULL=1) must.
	Reach            string   `json:"reach,omitempty"`
	TransitiveCap    int      `json:"transitive_cap,omitempty"`
	Deferred         []string `json:"deferred"`
	FullGateRequired bool     `json:"full_gate_required"`
	// LockfileChecks are the lockfile-<frontend> rows the diff adds;
	// ClosureHits, per check with paths_go_closure, the changed files in
	// its Go build closure, and ClosureErrors why one was not computed.
	LockfileChecks []string            `json:"lockfile_checks"`
	ClosureHits    map[string][]string `json:"closure_hits,omitempty"`
	ClosureErrors  map[string]string   `json:"closure_errors,omitempty"`
	// Modules are the nested replaced modules' tests
	// (ci.gate.affected_modules); always a list.
	Modules []ModuleTests `json:"modules"`
	// Checks are the path-gated rows (`paths`, `paths_go_closure`) and
	// whether these changes run each; only `forge ci gate --plan` sets it.
	Checks []PlanCheck `json:"checks,omitempty"`
}

// PlanCheck is one path-gated row's decision in a plan.
type PlanCheck struct {
	Name string `json:"name"`
	Runs bool   `json:"runs"`
	Note string `json:"note,omitempty"`
}

// Document renders a for the --json document; nil for a nil a.
func (a *Affected) Document() *DocumentAffected {
	if a == nil {
		return nil
	}
	nonNil := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	d := &DocumentAffected{Full: a.Full, Reason: a.Reason, BaseRef: a.BaseRef, Base: a.Base, Total: a.Total,
		Changed: nonNil(a.Changed), Packages: nonNil(a.Packages), Build: nonNil(a.Build), Frontends: nonNil(a.Frontends),
		Why: a.Why, Files: a.Files, Selections: nonNil(a.Selections), Line: a.Line(),
		Reach: a.Reach, TransitiveCap: a.TransitiveCap, Deferred: nonNil(a.Deferred), FullGateRequired: len(a.Deferred) > 0,
		LockfileChecks: nonNil(a.LockfileChecks), ClosureHits: a.ClosureHits, ClosureErrors: a.ClosureErrs, Modules: a.Modules}
	if d.Modules == nil {
		d.Modules = []ModuleTests{}
	}
	if d.Why == nil {
		d.Why = []PackageWhy{}
	}
	if d.Files == nil {
		d.Files = []FileReport{}
	}
	return d
}
