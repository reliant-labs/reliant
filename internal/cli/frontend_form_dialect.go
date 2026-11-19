package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/codegen"
)

// zod4MigrationSkill is the playbook that moves a frontend from zod 3 to
// zod 4. `forge generate` names it when it writes a page for zod 3, and
// TestZod4Migration_Detection fails if it is not shipped.
const zod4MigrationSkill = "migrations/v1.8.0"

// frontendFormDialect reads the zod and @hookform/resolvers majors the
// frontend at frontendAbsDir uses, and answers the form dialect its
// generated create/edit pages must be written in. It is the ONE reading of
// those majors: the v1.8.0 migration's detection (`forge-detect
// pre-zod4-forms`) asks it too, so `forge generate` and `forge project
// upgrade list` cannot disagree about which frontends are still on zod 3.
//
// The frontend's package.json is its owner's: a frontend scaffolded before
// forge moved to zod 4 still declares zod 3 until the owner bumps it
// (migrations/v1.8.0). A page forge adds to it in the meantime — a new
// entity's create form — has to compile against what it uses.
//
// A package's major is the one its declared range admits (dependencies
// first, then devDependencies). A range that admits several majors
// (`>=3.0.0`, `*`, `^3 || ^4`) or none forge can read (`catalog:`,
// `latest`, a git spec) is settled by what is installed: node_modules (as
// Node resolves it, walking up), then the frontend's package-lock.json or
// pnpm-lock.yaml. When neither settles it the page gets today's default —
// zod 4, resolvers 5 on — and the returned warnings say so, naming the
// package.json. No package.json, or no such dependency, is the default
// without a warning: there is nothing to have misread.
func frontendFormDialect(frontendAbsDir string) (codegen.FormDialect, []string) {
	pkgPath := filepath.Join(frontendAbsDir, "package.json")
	raw, err := os.ReadFile(pkgPath) //nolint:gosec // a frontend dir + fixed filename
	if err != nil {
		return codegen.FormDialect{}, nil
	}
	var pkg packageJSONDeps
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return codegen.FormDialect{}, []string{fmt.Sprintf(
			"%s is not valid JSON (%v); generated forms assume zod 4 and @hookform/resolvers 5 or later", pkgPath, err)}
	}
	var warnings []string
	major := func(name, assumed string) (int, bool) {
		spec, declared := pkg.spec(name)
		if !declared {
			return 0, false
		}
		if m, ok := declaredMajor(spec); ok {
			return m, true
		}
		if m, ok := installedMajor(frontendAbsDir, name); ok {
			return m, true
		}
		warnings = append(warnings, fmt.Sprintf(
			"%s declares %s %q, which names no single major, and no installed %s was found; generated forms assume %s (pin a major, or install the frontend's packages, then generate again)",
			pkgPath, name, spec, name, assumed))
		return 0, false
	}
	zod, zodOK := major("zod", "zod 4")
	resolvers, resolversOK := major("@hookform/resolvers", "@hookform/resolvers 5 or later")
	return codegen.FormDialect{
		Zod3:           zodOK && zod < 4,
		LegacyResolver: resolversOK && resolvers < 5,
	}, warnings
}

// packageJSONDeps is the part of a package.json a dialect reading needs.
type packageJSONDeps struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// spec is the range name is declared with, and whether it is declared.
func (p packageJSONDeps) spec(name string) (string, bool) {
	if v, ok := p.Dependencies[name]; ok {
		return v, true
	}
	v, ok := p.DevDependencies[name]
	return v, ok
}

// declaredMajor reads the ONE major a package.json range admits: "^3.23.0",
// "~3.25", "3.x", "3", "v3.23.8", "=3.1.0", ">=3.0.0 <4", "3.0.0 - 3.25.76",
// "npm:zod@^3.22" and "workspace:^3.1" all admit only 3. It reports false
// for a range that admits several majors ("*", ">=3.0.0", "^3 || ^4") —
// what installs then depends on the registry, not the declaration — and for
// a spec that is not a version range at all (`latest`, `catalog:`,
// `workspace:*`, a git, file or URL spec).
func declaredMajor(spec string) (int, bool) {
	lo, hi, ok := rangeMajors(spec)
	if !ok || lo != hi {
		return 0, false
	}
	return lo, true
}

// unboundedMajor is rangeMajors' hi for a range with no upper bound.
const unboundedMajor = math.MaxInt

var (
	// comparatorRE is one node-semver comparator: an optional operator, an
	// optional `v`, and a partial version whose parts may be x-ranges. A
	// prerelease or build suffix is accepted and does not change the major.
	comparatorRE = regexp.MustCompile(`^(<=|>=|<|>|=|\^|~>|~)?v?(\d+|[xX*])(?:\.(\d+|[xX*])(?:\.(\d+|[xX*])([-+][0-9A-Za-z.+-]*)?)?)?$`)
	// hyphenRangeRE is `A - B` (spaces required around the hyphen).
	hyphenRangeRE = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)$`)
	// operatorSpaceRE joins an operator to its version: npm reads ">= 3" as ">=3".
	operatorSpaceRE = regexp.MustCompile(`(<=|>=|<|>|=|\^|~>|~)\s+`)
)

// rangeMajors reads an npm version range (node-semver grammar: comparator
// sets joined by `||`, each a space-separated intersection of comparators
// or an `A - B` hyphen range) and reports the lowest and highest major it
// admits; hi is unboundedMajor when nothing caps it. ok is false when spec
// is not a range forge can read, or admits no version at all.
func rangeMajors(spec string) (lo, hi int, ok bool) {
	s := strings.TrimSpace(spec)
	// npm:<name>@<range> installs <name> at <range>: an alias whose range
	// is what this package resolves to. No range means its latest tag.
	if rest, found := strings.CutPrefix(s, "npm:"); found {
		at := strings.LastIndex(rest, "@")
		if at <= 0 {
			return 0, 0, false
		}
		s = strings.TrimSpace(rest[at+1:])
	}
	// pnpm's workspace:<range> links the workspace package if it satisfies
	// <range>; `workspace:*`, `workspace:^` and `workspace:~` take its
	// version, whatever it is.
	if rest, found := strings.CutPrefix(s, "workspace:"); found {
		switch rest = strings.TrimSpace(rest); rest {
		case "*", "^", "~":
			return 0, 0, false
		default:
			s = rest
		}
	}
	// An empty range is npm's "*".
	if s == "" {
		return 0, unboundedMajor, true
	}
	lo, hi = unboundedMajor, -1
	for _, set := range strings.Split(s, "||") {
		setLo, setHi, setOK := comparatorSetMajors(set)
		if !setOK {
			return 0, 0, false
		}
		if setLo > setHi {
			continue // admits nothing; contributes nothing to the union
		}
		lo, hi = min(lo, setLo), max(hi, setHi)
	}
	if lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

// comparatorSetMajors intersects one `||` alternative. setLo > setHi means
// the set admits nothing; ok is false when it does not parse.
func comparatorSetMajors(set string) (lo, hi int, ok bool) {
	set = strings.TrimSpace(set)
	if set == "" {
		return 0, unboundedMajor, true // `a || ` is `a || *`
	}
	if m := hyphenRangeRE.FindStringSubmatch(set); m != nil {
		fromLo, _, okFrom := comparatorMajors(">=" + strings.TrimPrefix(m[1], "="))
		_, toHi, okTo := comparatorMajors("<=" + strings.TrimPrefix(m[2], "="))
		return fromLo, toHi, okFrom && okTo
	}
	lo, hi = 0, unboundedMajor
	for _, c := range strings.Fields(operatorSpaceRE.ReplaceAllString(set, "$1")) {
		cLo, cHi, cOK := comparatorMajors(c)
		if !cOK {
			return 0, 0, false
		}
		lo, hi = max(lo, cLo), min(hi, cHi)
	}
	return lo, hi, true
}

// comparatorMajors is the major interval one comparator admits.
func comparatorMajors(c string) (lo, hi int, ok bool) {
	m := comparatorRE.FindStringSubmatch(c)
	if m == nil {
		return 0, 0, false
	}
	op, majorPart, minorPart, patchPart, suffix := m[1], m[2], m[3], m[4], m[5]
	if isXRange(majorPart) {
		// "*", "x", ">=x": any version (node-semver reads "<x" as nothing,
		// which no real package.json declares).
		return 0, unboundedMajor, true
	}
	major, err := strconv.Atoi(majorPart)
	if err != nil {
		return 0, 0, false
	}
	// partial: the comparator stops at the major ("3", "3.x", "3.*").
	partial := minorPart == "" || isXRange(minorPart)
	switch op {
	case "", "=", "^", "~", "~>":
		return major, major, true
	case ">=":
		return major, unboundedMajor, true
	case ">":
		if partial {
			return major + 1, unboundedMajor, true // >3 is >=4.0.0
		}
		return major, unboundedMajor, true // >3.2.1 still admits 3.2.2
	case "<=":
		return 0, major, true // <=3 is <4.0.0; <=3.2.1 admits 3.x
	case "<":
		// <3, <3.x, <3.0 and <3.0.0 admit nothing of major 3; <3.0.1 and
		// <3.0.0-beta (a prerelease of 3.0.0 sorts below it) do.
		// "-0" is the lowest prerelease there is, so <4.0.0-0 admits no 4.x
		// either; a build suffix ("+meta") never orders.
		prerelease := strings.HasPrefix(suffix, "-") && suffix != "-0"
		minorZero := minorPart == "0" && (patchPart == "" || isXRange(patchPart) || patchPart == "0")
		if partial || (minorZero && !prerelease) {
			return 0, major - 1, true
		}
		return 0, major, true
	}
	return 0, 0, false
}

func isXRange(part string) bool { return part == "x" || part == "X" || part == "*" }

// installedMajor is the major of the name actually installed for the
// frontend at dir: node_modules first, resolved the way Node resolves it
// (dir, then each parent — a workspace hoists to its root), then the
// frontend's own package-lock.json, then its pnpm-lock.yaml.
func installedMajor(dir, name string) (int, bool) {
	for d := dir; ; {
		raw, err := os.ReadFile(filepath.Join(d, "node_modules", filepath.FromSlash(name), "package.json")) //nolint:gosec // resolved like Node does
		if err == nil {
			var p struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(raw, &p) == nil {
				if m, ok := exactMajor(p.Version); ok {
					return m, true
				}
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if v := packageLockVersion(dir, name); v != "" {
		if m, ok := exactMajor(v); ok {
			return m, true
		}
	}
	if v := pnpmLockVersion(dir, name); v != "" {
		if m, ok := exactMajor(v); ok {
			return m, true
		}
	}
	return 0, false
}

// packageLockVersion reads name's resolved version from dir/package-lock.json:
// lockfile v2/v3 `packages["node_modules/<name>"]`, else v1 `dependencies`.
func packageLockVersion(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "package-lock.json")) //nolint:gosec // a frontend dir + fixed filename
	if err != nil {
		return ""
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if json.Unmarshal(raw, &lock) != nil {
		return ""
	}
	if p, ok := lock.Packages["node_modules/"+name]; ok {
		return p.Version
	}
	return lock.Dependencies[name].Version
}

// pnpmLockVersion reads name's resolved version from dir/pnpm-lock.yaml:
// lockfile v6+ `importers["."]` (a single-project lockfile writes the root
// importer there too), else the v5 top-level dependency maps, whose values
// are the version itself.
func pnpmLockVersion(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "pnpm-lock.yaml")) //nolint:gosec // a frontend dir + fixed filename
	if err != nil {
		return ""
	}
	type importer struct {
		Dependencies    map[string]yaml.Node `yaml:"dependencies"`
		DevDependencies map[string]yaml.Node `yaml:"devDependencies"`
	}
	var lock struct {
		Importers       map[string]importer  `yaml:"importers"`
		Dependencies    map[string]yaml.Node `yaml:"dependencies"`
		DevDependencies map[string]yaml.Node `yaml:"devDependencies"`
	}
	if yaml.Unmarshal(raw, &lock) != nil {
		return ""
	}
	version := func(imp importer) string {
		for _, deps := range []map[string]yaml.Node{imp.Dependencies, imp.DevDependencies} {
			n, ok := deps[name]
			if !ok {
				continue
			}
			if n.Kind == yaml.ScalarNode {
				return n.Value // v5: `zod: 3.22.4`
			}
			var dep struct {
				Version string `yaml:"version"`
			}
			if n.Decode(&dep) == nil {
				return dep.Version // v6+: `zod: {specifier: ^3, version: 3.22.4}`
			}
		}
		return ""
	}
	if imp, ok := lock.Importers["."]; ok {
		if v := version(imp); v != "" {
			return v
		}
	}
	return version(importer{Dependencies: lock.Dependencies, DevDependencies: lock.DevDependencies})
}

// exactMajor is the major of an installed version: "3.25.76",
// "3.25.76(react@19.1.0)" (pnpm's peer suffix), "v4.1.0".
func exactMajor(v string) (int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 || end == len(v) || v[end] != '.' {
		return 0, false
	}
	m, err := strconv.Atoi(v[:end])
	return m, err == nil
}
