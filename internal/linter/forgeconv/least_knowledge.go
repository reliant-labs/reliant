// least_knowledge.go — the composition graph's least-knowledge rules
// (docs/design/composition-graph.md §2, K1/K2/K3/P1), over every component
// package's Deps as internal/depsurface reads it:
//
//	forgeconv-deps-surface      an interface Deps field declares methods the
//	                            package never needs through it: never calls,
//	                            and never passes on as an interface that
//	                            declares them (K1)
//	forgeconv-no-container-deps a Deps field holds a container: the
//	                            composition root's types, the command
//	                            tree's Deps, a locator, `any` (K2)
//	forgeconv-deps-no-downcast  a type assertion turns a Deps field back into
//	                            a concrete type (P1)
//	forgeconv-no-getenv         the package reads the process environment
//	                            itself (K3)
//	forgeconv-deps-root-config  a Deps field holds the root *config.Config
//	                            instead of the component's own block (K3)
//
// Severity is the design's (§9.2), by default: deps-surface is an ERROR for a
// FOREIGN interface (declared in another package) of more than ten methods
// with unused ones, and a warning otherwise; a container and a downcast are
// errors. They landed as warnings (PR 5) and became errors one release later,
// as §7 planned — a fresh scaffold has none of them.
//
// The two K3 rules are newer and land as PR 5's did. no-getenv is a warning
// (an error under --strict): a fresh scaffold reads no environment outside
// pkg/config. deps-root-config is reported only under --strict, and only as
// a warning, because the scaffold still hands every service, worker and
// package the root config: a finding every fresh project carries is one
// nobody reads. It becomes a default finding, then an error, once the
// scaffold births a config block per component.
//
// --strict makes every other least-knowledge finding an error.

package forgeconv

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/depsurface"
)

// Rule ids.
const (
	RuleDepsSurface     = "forgeconv-deps-surface"
	RuleNoContainerDeps = "forgeconv-no-container-deps"
	RuleDepsNoDowncast  = "forgeconv-deps-no-downcast"
	RuleNoGetenv        = "forgeconv-no-getenv"
	RuleDepsRootConfig  = "forgeconv-deps-root-config"
)

// depsSurfaceWideMethods is the foreign interface size past which an unused
// method is an error (§9.2).
const depsSurfaceWideMethods = 10

// LintLeastKnowledge runs the least-knowledge rules over the component
// packages under root/internal.
func LintLeastKnowledge(root string, opts LintOptions) (Result, error) {
	dirs, err := depsurface.FindComponentDirs(root)
	if err != nil {
		return Result{}, err
	}
	pkgs, err := depsurface.Analyze(root, dirs)
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, p := range pkgs {
		res.Findings = append(res.Findings, leastKnowledgeFindings(p, opts.Strict)...)
	}
	return res, nil
}

func leastKnowledgeFindings(p *depsurface.Package, strict bool) []Finding {
	// sev is a finding's severity: an error when the design gates on it, or
	// under --strict; a warning otherwise.
	sev := func(gates bool) Severity {
		if strict || gates {
			return SeverityError
		}
		return SeverityWarning
	}
	var out []Finding
	for _, f := range p.Fields {
		if f.Container != "" {
			out = append(out, Finding{
				Rule: RuleNoContainerDeps, Severity: sev(true), File: f.File, Line: f.Line,
				Message:     fmt.Sprintf("Deps.%s (%s) is %s — a component receives the capabilities it uses, never a container to fetch them from", f.Name, f.Type, f.Container),
				Remediation: "take each collaborator as its own typed field (an interface declared here, holding the methods the package calls), and configuration as the component's own config block; internal/app/compose.go fills each by type",
			})
		}
		if f.RootConfig && strict {
			out = append(out, Finding{
				Rule: RuleDepsRootConfig, Severity: SeverityWarning, File: f.File, Line: f.Line,
				Message:     fmt.Sprintf("Deps.%s (%s) is the root config — every field of every component — where the component needs its own block", f.Name, f.Type),
				Remediation: "declare the fields this component reads as a config message referenced from the root config (a block), take `Config config.<Block>` here, and fill it with `g.Cfg.<Block>` in internal/app/compose.go",
			})
		}
		unused := depsurface.Unused(f)
		if !f.Interface || len(unused) == 0 || f.Escapes || f.Downcast {
			continue
		}
		wide := f.Foreign && len(f.Methods) > depsSurfaceWideMethods
		where := "declared in this package"
		fix := fmt.Sprintf("delete the methods this package never uses from %s", f.Type)
		if f.Foreign {
			where = "declared in another package"
			fix = fmt.Sprintf("`forge fix narrow-deps %s` declares the %d-method interface the package uses, here, and retypes the field (compose.go needs no change)", p.Dir, len(f.Used))
			if len(f.Roles) > 0 {
				fix = fmt.Sprintf("`forge fix narrow-deps %s` declares the %d-method interface the package uses, here — embedding the interfaces it passes the value on as — and retypes the field (compose.go needs no change)", p.Dir, len(f.Used))
			}
		}
		uses := "calls"
		if len(f.Roles) > 0 {
			roles := make([]string, len(f.Roles))
			for i, r := range f.Roles {
				roles[i] = r.Type
			}
			uses = "uses (calls, or passes it on as " + summarize(roles, 4) + ")"
		}
		msg := fmt.Sprintf("Deps.%s (%s, %d methods, %s) — the package %s %d of them through it; unused: %s", f.Name, f.Type, len(f.Methods), where, uses, len(f.Used), summarize(unused, 6))
		if len(f.Used) == 0 {
			msg = fmt.Sprintf("Deps.%s (%s) is never called through — the package does not need it", f.Name, f.Type)
			fix = "delete the field (and its key in internal/app/compose.go)"
		}
		out = append(out, Finding{Rule: RuleDepsSurface, Severity: sev(wide), File: f.File, Line: f.Line, Message: msg, Remediation: fix})
	}
	for _, e := range p.EnvReads {
		out = append(out, Finding{
			Rule: RuleNoGetenv, Severity: sev(false), File: e.File, Line: e.Line,
			Message:     fmt.Sprintf("%s in a component package — configuration the composition never handed it, which its root's config reach (and so its workload's env) cannot see", e.Call),
			Remediation: "declare the setting in proto/<project>/config/v1 (a field, or a field of this component's block) and take it through Deps; pkg/config reads the environment, once, for every root",
		})
	}
	for _, d := range p.Downcasts {
		out = append(out, Finding{
			Rule: RuleDepsNoDowncast, Severity: sev(true), File: d.File, Line: d.Line,
			Message:     fmt.Sprintf("a type assertion turns Deps.%s back into %s — the field's interface is the capability the component was granted, and the assertion takes the rest", d.Field, d.Target),
			Remediation: fmt.Sprintf("declare the methods of %s the package needs on the field's interface (or a second field), so the composition grants them explicitly", d.Target),
		})
	}
	return out
}

func summarize(names []string, limit int) string {
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:limit], ", "), len(names)-limit)
}
