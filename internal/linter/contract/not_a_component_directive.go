package contract

// The not-a-component gate — what `//forge:not-a-component` costs.
//
// The marker takes a contract package out of the server composition: compose.go
// never constructs it, never resolves its Deps, and gives it no Infra field.
// The contract, the mock and (when it opts in) the decorator stay. It exists
// for packages the server genuinely does not have — CLI login flows, daemon
// credential stores — which used to fail `forge generate` because every
// contract package was assumed to be a server component.
//
// It does NOT exempt the package from a single contract rule, so it is not a
// way to turn lint green, and the exclusion gate's I/O and multi-impl
// refusals do not apply. Its cost is the one every opt-out of a default pays:
//
//  1. A REASON. `//forge:not-a-component: <why>`. The default is that a
//     contract package is a server component; a reader of contract.go who
//     finds it missing from compose.go needs to know who builds it instead.
//     A bare marker still works for codegen (it never breaks over a spelling)
//     but lint refuses it, exactly like a bare `//forge:exclude-contract`.
//
//  2. BEING WHERE IT MEANS SOMETHING. Composition honours the marker only on
//     a contract package under internal/ that forge would otherwise compose.
//     Anywhere else it is a claim nothing reads: a handler, worker or
//     operator is a server component by definition; a package without
//     contract.go (or opted out with exclude-contract, or outside internal/)
//     is never composed anyway; and next to `//forge:external-component` it
//     contradicts the other marker, which says the server DOES have the
//     package. A silently-ignored marker is how a tree starts lying, so each
//     of those is reported.
//
// The third way the marker can be false — a server component requiring the
// package by type — is a fact about the composition graph, so `forge
// generate` refuses it where that graph is built (internal/codegen
// notAComponentConsumers), not here.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Rule IDs of the not-a-component gate. Stable: they appear in findings and
// are the names a `forge:lint-disable` directive suppresses.
const (
	// RuleNotAComponentReason refuses a bare `//forge:not-a-component`.
	RuleNotAComponentReason = "forge-not-a-component-reason"
	// RuleNotAComponentScope refuses the marker where composition does not
	// read it, or where it contradicts another marker.
	RuleNotAComponentScope = "forge-not-a-component-scope"
)

// serverRoleRoots are the module-relative roots whose packages are server
// components by definition — mounted or supervised whether or not anything
// depends on them.
var serverRoleRoots = []struct{ root, role string }{
	{"internal/handlers/", "a service handler (it is mounted)"},
	{"internal/workers/", "a worker (it is supervised)"},
	{"internal/operators/", "an operator (it is supervised)"},
}

// CheckNotAComponentDirectives applies the not-a-component gate to every root
// package in pkgs. Findings reuse the exclusion gate's shape and suppression
// semantics: `// forge:lint-disable-next-line <rule>: <why>` in the marker's
// comment block.
func CheckNotAComponentDirectives(pkgs []*packages.Package, opts ExcludeGateOptions) []ExcludeFinding {
	dirs := map[string]*packages.Package{}
	for _, p := range pkgs {
		if p.Types == nil || strings.HasSuffix(p.PkgPath, "_test") || strings.HasSuffix(p.PkgPath, ".test") {
			continue
		}
		dir := pkgDir(p)
		if dir == "" {
			continue
		}
		if _, seen := dirs[dir]; seen && p.ID != p.PkgPath {
			continue // a test variant; the plain package carries the same source
		}
		dirs[dir] = p
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Strings(ordered)

	var out []ExcludeFinding
	for _, dir := range ordered {
		p := dirs[dir]
		markers := codegen.FindNotAComponentMarkers(dir)
		if len(markers) == 0 {
			continue
		}
		marker, conflict := packageDeclaration(p.PkgPath, "forge:not-a-component", markers,
			[]string{RuleNotAComponentReason, RuleNotAComponentScope}, RuleNotAComponentConflict)
		if conflict != nil {
			out = append(out, *conflict)
			continue
		}
		if marker.Reason == "" {
			out = append(out, ExcludeFinding{
				Rule:     RuleNotAComponentReason,
				Severity: finding.SeverityError,
				File:     marker.File,
				Line:     marker.Line,
				Message: fmt.Sprintf("package %s is taken out of the server composition with a bare "+
					"`//forge:not-a-component` — the marker requires a reason", p.PkgPath),
				FixHint: "say who constructs it instead of the server: `//forge:not-a-component: <why>` " +
					"(e.g. \"CLI-only: built by the login command\"). If the server does use it, delete the marker — " +
					"see `forge skill load contracts/package-markers`",
			})
		}
		// Placement is about what forge's composition reads, and there is no
		// composition in a tree forge does not generate.
		if opts.CodegenUnavailable {
			continue
		}
		if why := notAComponentMisplaced(p, dir); why != "" {
			out = append(out, ExcludeFinding{
				Rule:     RuleNotAComponentScope,
				Severity: finding.SeverityError,
				File:     marker.File,
				Line:     marker.Line,
				Message:  fmt.Sprintf("`//forge:not-a-component` on package %s %s", p.PkgPath, why),
				FixHint: "delete the marker. It applies only to an internal/<pkg> contract package (one with " +
					"contract.go) that the server should not construct — see `forge skill load contracts/package-markers`",
			})
		}
	}
	return applyDirectiveSuppressions(out)
}

// notAComponentMisplaced returns why the marker in dir has no effect or
// contradicts the package's other markers, or "" when it is where
// composition reads it.
func notAComponentMisplaced(p *packages.Package, dir string) string {
	rel := moduleRelDir(p, dir)
	for _, r := range serverRoleRoots {
		// The role's own package is internal/<role>/<leaf>. A contract
		// package nested BELOW one is discovered as an ordinary internal
		// package, and composition honours the marker there.
		if leaf, ok := strings.CutPrefix(rel, r.root); ok && leaf != "" && !strings.Contains(leaf, "/") {
			return "contradicts what the package is: " + r.role + " is a server component by definition, " +
				"and composition ignores the marker there"
		}
	}
	if codegen.HasExternalComponentDirective(dir) {
		return "contradicts its `//forge:external-component` marker: that one says the server HAS the package " +
			"(hand-built on Infra), this one says it does not — keep the one that is true"
	}
	if !strings.HasPrefix(rel, "internal/") {
		return "has no effect: forge composes only contract packages under internal/"
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); err != nil {
		return "has no effect: the package has no contract.go, so it is not a component to begin with"
	}
	if codegen.HasExcludeContractDirective(dir) {
		return "has no effect: `//forge:exclude-contract` already takes the package out of contract codegen " +
			"and composition both"
	}
	return ""
}

// moduleRelDir returns dir relative to p's module root, in forward-slash
// form. Without module information it falls back to the import path's
// "internal/" suffix, which is what every check above keys on.
func moduleRelDir(p *packages.Package, dir string) string {
	if p.Module != nil && p.Module.Dir != "" {
		if rel, err := filepath.Rel(p.Module.Dir, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if i := strings.Index(p.PkgPath, "/internal/"); i >= 0 {
		return p.PkgPath[i+1:]
	}
	return p.PkgPath
}
