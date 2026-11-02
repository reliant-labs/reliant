// File: uninstrumented.go — forge-tracing-contract-uninstrumented.
//
// A contract package's generated decorator (middleware_gen.go) is what turns
// each Service method call into a span, so the SQL and outbound calls the
// method makes nest under it. compose.go is kept wrapped by `forge generate`;
// a construction anywhere else — providers.go, a *_wire.go file, a second
// binary, a package composing its own sub-services — is not, and the package's
// methods vanish from every trace that goes through that value. In the control
// plane, after every contract package gained a decorator, 46 constructions in
// 18 files still used the bare New.
//
// Flagged: a call to `<alias>.New(…)` of an instrumented package (the same
// decision `forge generate` makes, supplied by the caller) from outside that
// package, whose result is neither passed straight to a …WithForgeMiddleware
// constructor nor bound to a name that the same function later passes to one.
//
// Fixed by renaming New to NewInstrumented — the decorated twin middleware_gen.go
// declares with New's exact signature (internal/generator/contract,
// observed_ctor.go) — which is valid in every position a call can take and
// changes no type, so the fix cannot break a build the way inserting a
// wrapper around a fallible New would. The reverse is flagged and fixed too: a
// NewInstrumented whose package opted out (no decorator any more) goes back
// to New.

package tracinglint

import (
	"errors"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// ctorSite is one construction the rule judges: either an unwrapped
// `pkg.New(…)` of an instrumented package, or a stale `pkg.NewInstrumented(…)`
// of a package that no longer generates one.
type ctorSite struct {
	sel     *ast.SelectorExpr // the call's pkg.Ident selector
	alias   string
	pkg     string // package dir base name, for the message
	stale   bool
	rename  string // the identifier the fix writes in place of sel.Sel, "" when unfixable
	wrapper string // the decorator constructor, for the manual remediation
}

func (s ctorSite) finding(f *goFile) finding.Finding {
	if s.stale {
		return finding.Finding{
			Rule:        RuleContractUninstrumented,
			Severity:    finding.SeverityError,
			File:        f.rel,
			Line:        f.line(s.sel.Pos()),
			Message:     fmt.Sprintf("%s.%s no longer exists: the %s package generates no decorator (it opted out with forge:no-observe)", s.alias, InstrumentedCtorName, s.pkg),
			Remediation: "`forge lint --tracing --fix` rewrites it back to " + s.alias + ".New",
		}
	}
	rem := "wrap it: " + s.alias + "." + s.wrapper + "(" + s.alias + ".New(…)) — or opt the package out with a package-level `// forge:no-observe`"
	if s.rename != "" {
		rem = "call " + s.alias + "." + InstrumentedCtorName + " (New's signature, decorated); `forge lint --tracing --fix` rewrites it — or opt the package out with a package-level `// forge:no-observe`"
	}
	return finding.Finding{
		Rule:     RuleContractUninstrumented,
		Severity: finding.SeverityError,
		File:     f.rel,
		Line:     f.line(s.sel.Pos()),
		Message: fmt.Sprintf("%s.New constructs the %s contract without its generated decorator, so its methods open no span and the work under them is parentless in the trace",
			s.alias, s.pkg),
		Remediation: rem,
	}
}

// InstrumentedCtorName is the decorated twin of New that middleware_gen.go
// declares (internal/generator/contract: InstrumentedCtorName).
const InstrumentedCtorName = "NewInstrumented"

// uninstrumentedSites finds the constructions in f the rule judges.
func uninstrumentedSites(f *goFile, root, module string, instrumented func(dir string) bool) []ctorSite {
	ownDir := filepath.Dir(f.abs)
	type target struct {
		dir       string
		decorator string
		hasInstr  bool
	}
	targets := map[string]target{} // local name → package
	for name, path := range f.imports {
		if !strings.HasPrefix(path, module+"/") {
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(path, module+"/")))
		if dir == ownDir {
			continue
		}
		dec, hasDec := declaredDecorator(dir)
		// Same rule compose.go's wrapping follows: a package is wrapped only
		// once its middleware_gen.go declares the decorator. A package with
		// none is still a target, for stale NewInstrumented calls.
		if hasDec && !instrumented(dir) {
			continue
		}
		targets[name] = target{dir: dir, decorator: dec, hasInstr: hasDec && declaresInstrumentedCtor(dir)}
	}
	if len(targets) == 0 {
		return nil
	}
	var out []ctorSite
	forEachFunc(f.file, func(body ast.Node) {
		wrappedCalls := map[*ast.CallExpr]bool{}
		wrappedNames := map[string]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasSuffix(sel.Sel.Name, middlewareConstructorSuffix) {
				return true
			}
			switch a := call.Args[0].(type) {
			case *ast.CallExpr:
				wrappedCalls[a] = true
			case *ast.Ident:
				wrappedNames[a.Name] = true
			}
			return true
		})
		// Constructions bound to a name the function later wraps.
		boundWrapped := map[*ast.CallExpr]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := as.Lhs[0].(*ast.Ident); ok && wrappedNames[id.Name] {
				boundWrapped[call] = true
			}
			return true
		})
		ast.Inspect(body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || wrappedCalls[call] || boundWrapped[call] {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			tg, ok := targets[id.Name]
			if !ok {
				return true
			}
			site := ctorSite{sel: sel, alias: id.Name, pkg: filepath.Base(tg.dir), wrapper: tg.decorator}
			switch {
			case sel.Sel.Name == "New" && tg.decorator != "":
				if tg.hasInstr {
					site.rename = InstrumentedCtorName
				}
			case sel.Sel.Name == InstrumentedCtorName && tg.decorator == "":
				site.stale, site.rename = true, "New"
			default:
				return true
			}
			out = append(out, site)
			return true
		})
	})
	return out
}

// applyCtorFixes renames each fixable site's selector in place.
func applyCtorFixes(f *goFile, sites []ctorSite) (int, error) {
	type edit struct {
		at, end int
		text    string
	}
	var edits []edit
	for _, s := range sites {
		edits = append(edits, edit{
			at:   f.fset.Position(s.sel.Sel.Pos()).Offset,
			end:  f.fset.Position(s.sel.Sel.End()).Offset,
			text: s.rename,
		})
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].at > edits[j].at })
	out := append([]byte(nil), f.src...)
	for _, e := range edits {
		out = append(out[:e.at], append([]byte(e.text), out[e.end:]...)...)
	}
	if err := writeAndReload(f, out); err != nil {
		if errors.Is(err, errNotFixed) {
			return 0, nil
		}
		return 0, err
	}
	return len(edits), nil
}

var decoratorRE = regexp.MustCompile(`(?m)^func (New\w*` + middlewareConstructorSuffix + `)\(`)

// memoize caches the instrument decision per package dir for one run.
func memoize(instrumented func(string) bool) func(string) bool {
	memo := map[string]bool{}
	return func(dir string) bool {
		v, ok := memo[dir]
		if !ok {
			v = instrumented(dir)
			memo[dir] = v
		}
		return v
	}
}

// declaredDecorator is the wrapper constructor dir's middleware_gen.go
// declares, and whether it declares one.
func declaredDecorator(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "middleware_gen.go"))
	if err != nil {
		return "", false
	}
	m := decoratorRE.FindSubmatch(data)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// declaresInstrumentedCtor reports whether dir's middleware_gen.go declares
// NewInstrumented (a decorator generated before forge#1016 does not).
func declaresInstrumentedCtor(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "middleware_gen.go"))
	return err == nil && strings.Contains(string(data), "\nfunc "+InstrumentedCtorName+"(")
}
