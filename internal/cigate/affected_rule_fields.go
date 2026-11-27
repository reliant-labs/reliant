package cigate

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// The affected-rule fields beyond paths/packages/frontends/full:
//
//   - on: go-source matches every changed .go file, so a walker test (one
//     that reads the whole tree's Go source) runs on any Go change. Such a
//     match adds what the rule selects but claims nothing: a .go file outside
//     every package still runs the full suite.
//   - packages_importing selects the test packages whose test files import a
//     helper package (go list's TestImports, XTestImports).
//   - full_checks names gate rows that run whole when the rule matches.
//   - a `{dir}` in a packages pattern is the matched file's directory below
//     the matching glob's literal prefix: proto/x/v1/a.proto under
//     `proto/**` makes `./gen/{dir}` select ./gen/x/v1.

// DirPlaceholder is the packages-pattern placeholder for the matched file's
// directory.
const DirPlaceholder = "{dir}"

// ruleMatch reports whether rule matches changed file f, and whether it
// claims it (its paths match; an on: go-source match alone does not claim).
func ruleMatch(f string, rule config.CIGateAffectedRule) (matched, claims bool) {
	if matchGlobs(f, rule.Except) {
		return false, false
	}
	if matchGlobs(f, rule.Paths) {
		return true, true
	}
	return rule.On == config.CIGateOnGoSource && strings.HasSuffix(f, ".go"), false
}

// rulePackages are the test packages rule selects for the changed file f.
func (g *packageGraph) rulePackages(f string, rule config.CIGateAffectedRule) []string {
	var out []string
	for _, pat := range rule.Packages {
		if strings.Contains(pat, DirPlaceholder) {
			pat = expandDir(pat, f, rule.Paths)
		}
		out = append(out, g.resolve(pat)...)
	}
	return append(out, g.importers(rule.PackagesImporting)...)
}

// expandDir substitutes f's directory, below the literal prefix of the first
// glob in globs that matches f, for each {dir} in pat.
func expandDir(pat, f string, globs []string) string {
	f = filepath.ToSlash(f)
	dir := path.Dir(f)
	for _, g := range globs {
		if !matchGlobs(f, []string{g}) {
			continue
		}
		if prefix := literalPrefix(g); prefix != "" {
			if dir == prefix {
				dir = "."
			} else {
				dir = strings.TrimPrefix(dir, prefix+"/")
			}
		}
		break
	}
	out := path.Clean(strings.ReplaceAll(pat, DirPlaceholder, dir))
	if strings.HasPrefix(pat, "./") && !strings.HasPrefix(out, "./") && out != "." {
		out = "./" + out
	}
	return out
}

// literalPrefix is glob's leading directories that hold no wildcard:
// `proto/**` → proto, `api/*/v1/*.proto` → api, `*.md` → "".
func literalPrefix(glob string) string {
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(glob), "./"), "/")
	var lit []string
	for _, p := range parts[:len(parts)-1] {
		if strings.ContainsAny(p, "*?[") {
			break
		}
		lit = append(lit, p)
	}
	return strings.Join(lit, "/")
}

// importers are the module's test packages whose test files import one of
// helpers (import paths, or ./dir patterns naming one package).
func (g *packageGraph) importers(helpers []string) []string {
	if len(helpers) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, h := range helpers {
		h = filepath.ToSlash(strings.TrimSpace(h))
		if strings.HasPrefix(h, "./") || h == "." {
			if pkg, ok := g.dirPkg[filepath.Join(g.root, filepath.FromSlash(h))]; ok {
				want[pkg] = true
			}
			continue
		}
		want[h] = true
	}
	var out []string
	for pkg := range g.testDeps {
		for imp := range g.testImports[pkg] {
			if want[imp] {
				out = append(out, pkg)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// ruleFullChecks are the rows the rules' full_checks force whole for these
// changed files, sorted. It needs no package graph, so path-gated checks are
// settled by it also when the unit lane is not narrowed.
func ruleFullChecks(changed []string, rules []config.CIGateAffectedRule) []string {
	set := map[string]bool{}
	for _, rule := range rules {
		if len(rule.FullChecks) == 0 {
			continue
		}
		for _, f := range changed {
			if ok, _ := ruleMatch(f, rule); ok {
				for _, c := range rule.FullChecks {
					set[c] = true
				}
				break
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
