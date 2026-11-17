package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/internal/naming"
)

// IsWiredComponentDir reports whether dir is a forge-WIRED component — the
// single predicate that decides whether a package occupies a slot in the
// generated test-factory namespace (`app.NewTest<X>`).
//
// A wired component is a package forge itself constructs, which means all
// four of:
//
//   - it declares a contract.go (the Service/Deps/New triple lives there);
//   - it is not opted out with `//forge:exclude-contract` — a types-only
//     package (internal/db, internal/deploy) carries this precisely because
//     there is nothing to construct;
//   - it is not `//forge:external-component` — hand-built in providers.go /
//     OpenInfra, so forge emits no factory for it either;
//   - it is not `//forge:not-a-component` — the server composition does not
//     construct it at all (a CLI- or daemon-only package), so there is no
//     forge-wired instance for a factory to stand in for. It keeps its
//     mock_gen.go, which is the seam its consumers test against.
//
// WHY THIS EXISTS AS ONE FUNCTION. The name is derived by two generators —
// the helpers generator (which builds its namespace from real components) and
// ComputeTestHelperName (which drives the scaffold tests). They used to ask
// DIFFERENT questions: the helpers generator asked "is this a component",
// ComputeTestHelperName asked only `os.Stat(internal/<pkg>)`, "does a
// directory exist". A types-only internal/deploy answered no to the first and
// yes to the second, so testing.go declared NewTestDeploy while sixteen
// scaffold tests referenced NewTestSvcDeploy, and the package did not compile.
// Routing both through this predicate is what makes that disagreement
// unrepresentable rather than merely fixed once.
//
// The forge.yaml `contracts.exclude` list is deliberately NOT consulted here:
// it is a whole-subtree opt-out read by DiscoverInternalPackages, which
// removes those packages from the inventory before either generator sees
// them. A path that never reaches the namespace cannot collide with it.
func IsWiredComponentDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); err != nil {
		return false // no contract.go: nothing declares a component here
	}
	if HasExcludeContractDirective(dir) {
		return false // types-only / library shape, opted out by its own source
	}
	return !HasExternalComponentDirective(dir) && !HasNotAComponentDirective(dir)
}

// ComputeTestHelperName returns the suffix used by the `app.NewTest<X>` and
// `app.NewTest<X>Server` factories generated into pkg/app/testing.go. When
// the service's Go-package name collides with a WIRED internal component of
// the same name (e.g. service `billing` + component `internal/billing/`), the
// bootstrap testing generator disambiguates by prefixing "Svc"
// (NewTestSvcBilling). This helper mirrors that rule so test scaffolds emit
// the same identifier the factory actually has.
//
// The collision test is IsWiredComponentDir — "does this package occupy a
// factory-namespace slot" — not "does a directory of this name exist". Those
// are different questions, and answering the second one made this function
// disagree with the generator it is supposed to mirror; see IsWiredComponentDir.
//
// projectDir may be empty (no project context); in that case there's no
// collision detection possible and the result is the no-collision form.
func ComputeTestHelperName(servicePkg, projectDir string) string {
	pascal := naming.ToPascalCase(servicePkg)
	if projectDir == "" {
		return pascal
	}
	if IsWiredComponentDir(filepath.Join(projectDir, "internal", servicePkg)) {
		return "Svc" + pascal
	}
	return pascal
}

// WorkerSpec carries a worker's user-facing name plus the optional `path:`
// field declared in forge.yaml. When Path is non-empty, the dir leaf
// (`filepath.Base(Path)`) becomes the import-path segment and the Go-package
// alias — preserving the user's exact dir name. When Path is empty,
// behavior falls back to `naming.GoPackage(name)` which produces the
// snake_case canonical form (`calibrator_refit` stays `calibrator_refit`,
// `email-sender` becomes `email_sender`).
//
// OperatorSpec mirrors this for operators (same path-honoring rule).
type WorkerSpec struct {
	Name string // user-facing name from forge.yaml (e.g. "climatology_refresh")
	Path string // optional dir path from forge.yaml (e.g. "workers/climatology_refresh"); empty falls back to naming.GoPackage(name)
}

// OperatorSpec is the operator-side analog of WorkerSpec. See WorkerSpec for
// the path-honoring rationale.
type OperatorSpec struct {
	Name string
	Path string
}

// WorkerDataFromSpecs builds BootstrapWorkerData honoring each spec's
// optional `path:` field. When Path is set, the on-disk dir leaf is used
// for the import line — so snake_case dirs like
// `workers/climatology_refresh/` produce the import line
// `workers/climatology_refresh` — while the Go package alias still comes
// from the directory's REAL `package X` clause when the dir exists
// (disk-first; see disk_resolver.go).
//
// When Path is empty, the worker's EXISTING directory + package clause
// are resolved from disk (ResolveComponentDir), so `engine_shadow` keeps
// importing workers/engine_shadow with whatever package name that
// directory actually declares. Synthesis (`naming.GoPackage`'s snake_case
// canonical form) applies only when the directory doesn't exist yet —
// i.e. a brand-new scaffold.
//
// Returns an error when a worker directory exists but its package clause
// is unparseable/ambiguous (see ParsePackageClause) — guessing here is
// exactly the broken-imports bug class disk-first resolution eliminates.
//
// Cross-role collision (worker named `audit` vs internal/audit/) is still
// resolved by AssignBootstrapAliases prefixing one of the colliding aliases.
func WorkerDataFromSpecs(specs []WorkerSpec, projectDir string) ([]BootstrapWorkerData, error) {
	var workers []BootstrapWorkerData
	for _, spec := range specs {
		comp, err := componentDataFromSpec(spec.Name, spec.Path, projectDir, "internal/workers")
		if err != nil {
			return nil, err
		}
		workers = append(workers, comp)
	}
	return workers, nil
}

// WorkerDataFromNames is the legacy entry point — thin wrapper over
// WorkerDataFromSpecs with empty Path. Preserved for callers (and tests)
// that don't carry forge.yaml context. New code should use
// WorkerDataFromSpecs so the explicit `path:` field is honored.
//
// FieldName derives from the ORIGINAL name (which retains its separators)
// via ToPascalCase so snake_case worker names still produce idiomatic
// exported identifiers (`Workers.CalibratorRefit`,
// `wireWorkerCalibratorRefitDeps`) rather than the run-together
// `Workers.Calibratorrefit` shape.
func WorkerDataFromNames(names []string, projectDir string) ([]BootstrapWorkerData, error) {
	specs := make([]WorkerSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, WorkerSpec{Name: name})
	}
	return WorkerDataFromSpecs(specs, projectDir)
}

// BootstrapOperatorData is the operator spelling of BootstrapComponentData.
// Operators and workers render from the same shape; the alias keeps the
// operator-side call sites reading as operator code.
type BootstrapOperatorData = BootstrapComponentData

// OperatorDataFromSpecs is the operator-side analog of WorkerDataFromSpecs —
// honors `path:` when set so operator dirs with separator-bearing leaves
// (e.g. `operators/cert_rotator`) get the correct import line. See
// WorkerDataFromSpecs for the disk-first path/alias/error rules.
func OperatorDataFromSpecs(specs []OperatorSpec, projectDir string) ([]BootstrapOperatorData, error) {
	var operators []BootstrapOperatorData
	for _, spec := range specs {
		comp, err := componentDataFromSpec(spec.Name, spec.Path, projectDir, "internal/operators")
		if err != nil {
			return nil, err
		}
		operators = append(operators, comp)
	}
	return operators, nil
}

// OperatorDataFromNames builds BootstrapOperatorData from operator names (e.g. from forge.yaml).
// Thin wrapper over OperatorDataFromSpecs preserved for callers without
// forge.yaml context. See WorkerDataFromNames for the disk-first
// resolution + FieldName rationale (same snake_case → PascalCase rule).
func OperatorDataFromNames(names []string, projectDir string) ([]BootstrapOperatorData, error) {
	specs := make([]OperatorSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, OperatorSpec{Name: name})
	}
	return OperatorDataFromSpecs(specs, projectDir)
}

// componentDataFromSpec is the shared worker/operator builder: disk-first
// identity per component (real directory leaf for the import line, real
// package clause for aliases/selectors), synthesized identity only for
// not-yet-scaffolded names.
//
// When explicitPath (the forge.yaml `path:` field) is set, its dir leaf is
// used verbatim as the import segment — preserving the user's exact dir
// name. The package clause is still parsed from disk when the directory
// exists so the Alias matches the ground truth (e.g. workers/widget_v2
// declaring legacy `package widgetv2`); a directory that exists but whose
// clause is unparseable/conflicting is a hard error, never a silent
// synthesis fallback.
func componentDataFromSpec(name, explicitPath, projectDir, roleRoot string) (BootstrapComponentData, error) {
	var res ResolvedComponent
	if explicitPath != "" { //nolint:nestif // explicit `path:` resolution: the arms are the disk-first fallback order, and flattening them loses which rule matched.
		leaf := filepath.Base(filepath.FromSlash(explicitPath))
		res = ResolvedComponent{
			Dir:         filepath.Join(projectDir, roleRoot, leaf),
			ImportLeaf:  leaf,
			PackageName: leaf,
			FromDisk:    false,
		}
		if projectDir != "" {
			if fi, statErr := os.Stat(res.Dir); statErr == nil && fi.IsDir() {
				pkg, perr := ParsePackageClause(res.Dir)
				if perr != nil {
					return BootstrapComponentData{}, fmt.Errorf("resolving %s component %q: %w", roleRoot, name, perr)
				}
				res.PackageName = pkg
				res.FromDisk = true
			}
		}
	} else {
		var err error
		res, err = ResolveComponentDir(projectDir, roleRoot, name)
		if err != nil {
			return BootstrapComponentData{}, err
		}
	}
	fieldName := naming.ToPascalCase(name)
	fallible := false
	ctor := DefaultConstructorName
	if res.FromDisk {
		fallible, _ = DetectFallibleConstructor(res.Dir)
		ctor = DetectConstructorName(res.Dir)
	}
	return BootstrapComponentData{
		Name:        name,
		Package:     res.PackageName,
		ImportPath:  res.ImportLeaf,
		FieldName:   fieldName,
		VarName:     lowerFirst(fieldName),
		Fallible:    fallible,
		Constructor: ctor,
		Alias:       res.PackageName,
	}, nil
}

// lowerFirst returns s with the first rune lowercased — used to derive a
// lowerCamel local-variable prefix from a PascalCase FieldName so generated
// bootstrap code avoids collisions when nested packages share a leaf name.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// upperFirst returns s with the first rune uppercased — used to build
// alias prefixes (e.g. "svc" + "Billing").
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// CollisionCounts returns a map of Go-package-name → occurrence count
// across services, packages, workers, and operators. A count > 1 means
// a cross-role collision (e.g. service "billing" + internal package
// "billing") and is the trigger for role-prefixed aliasing in
// AssignBootstrapAliases. Exposed so wire_gen and other generators can
// derive the SAME collision-aware FieldName that bootstrap uses without
// duplicating the bookkeeping.
func CollisionCounts(services []BootstrapServiceData, packages []BootstrapPackageData, workers []BootstrapWorkerData, operators []BootstrapOperatorData) map[string]int {
	count := map[string]int{}
	for _, s := range services {
		count[s.Package]++
	}
	for _, p := range packages {
		count[p.Package]++
	}
	for _, w := range workers {
		count[w.Package]++
	}
	for _, o := range operators {
		count[o.Package]++
	}
	return count
}

// ResolveCollisionNaming returns the (Alias, FieldName) pair for a
// component given its raw Package name, a fallback FieldName the caller
// already computed for the no-collision case, the cross-role collision
// counts, and the component's role-short-name prefix ("svc", "pkg",
// "wkr", "op"). When the package collides cross-role, the result is
// (rolePrefix + Package, RolePrefix + Package) — alias is lower-camel,
// field name is upper-camel. Otherwise (Package, fallbackFieldName) —
// preserving the caller's per-role naming convention (services use
// ToPascalCase; workers/operators also use ToPascalCase so snake_case
// names produce idiomatic exported identifiers (`Workers.CalibratorRefit`
// rather than `Workers.Calibrator_refit`); nested packages use a
// path-encoded form via ToExportedFieldName).
//
// Single source of truth for the wire_gen ↔ bootstrap naming agreement:
// both files derive their `wireXxxDeps` function name + `Services.Xxx`
// field reference from this helper, so the two stay in lockstep when a
// service package collides with an internal-package import.
func ResolveCollisionNaming(pkg, fallbackFieldName, rolePrefix string, counts map[string]int) (alias, fieldName string) {
	if counts[pkg] > 1 {
		return rolePrefix + upperFirst(pkg), upperFirst(rolePrefix) + upperFirst(pkg)
	}
	return pkg, fallbackFieldName
}

// AssignBootstrapAliases populates the Alias field on every
// BootstrapComponentData across services, packages, workers, and
// operators. When the .Package fields are unique across all four roles,
// each Alias equals its Package (default Go import alias — preserves the
// original codegen output). When two roles share a .Package value (e.g.
// service "billing" + internal package "billing"), the conflicting
// component(s) get a role-prefixed alias ("svcBilling", "pkgBilling",
// "wkrBilling", "opBilling") so the import line in bootstrap.go can
// alias the import and every reference site can use the alias unambiguously.
//
// This is purely additive — when there's no collision the default
// alias matches Package and the rendered bootstrap is identical to
// pre-aliasing output.
//
// Internally delegates to ResolveCollisionNaming so wire_gen and
// bootstrap derive their function/field names from the same rule.
func AssignBootstrapAliases(services []BootstrapServiceData, packages []BootstrapPackageData, workers []BootstrapWorkerData, operators []BootstrapOperatorData) {
	count := CollisionCounts(services, packages, workers, operators)

	setAlias := func(c *BootstrapComponentData, rolePrefix string) {
		alias, fieldName := ResolveCollisionNaming(c.Package, c.FieldName, rolePrefix, count)
		c.Alias = alias
		// Only override FieldName/VarName on collision — the no-collision
		// branch keeps whatever the caller computed (services and
		// workers/operators use ToPascalCase; packages use
		// ToExportedFieldName for nested support; honoring those preserves
		// nested-package field names like "McpDatabase").
		if count[c.Package] > 1 {
			c.FieldName = fieldName
			c.VarName = lowerFirst(c.FieldName)
		}
	}
	for i := range services {
		setAlias(&services[i], "svc")
	}
	for i := range packages {
		setAlias(&packages[i], "pkg")
	}
	for i := range workers {
		setAlias(&workers[i], "wkr")
	}
	for i := range operators {
		setAlias(&operators[i], "op")
	}
}

// PackageDataFromNames builds BootstrapPackageData from package names (e.g. from
// forge.yaml or discoverPackages). Names may be flat ("cache") or nested using
// forward slashes ("mcp/database"). For nested names the leaf segment is the Go
// package identifier (used at call sites like `database.New(...)`), while the
// full path is preserved for the import line and for deriving a unique
// FieldName/VarName so two leaves with the same name (e.g. "mcp/database" and
// "foo/database") don't collide in generated code.
//
// projectDir is the root project directory; if non-empty, it is used to detect
// fallible constructors by inspecting the Go source in internal/<importPath>/,
// and — disk-first — to read the leaf package's REAL package clause so the
// generated alias/selector can never disagree with what internal/<importPath>
// actually declares (a snake_case dir like internal/email_sender may declare
// either `package email_sender` or `package emailsender`; only the file on
// disk knows which). Synthesis of the leaf name applies only when the
// directory doesn't exist (e.g. unit tests passing bare name lists).
//
// Returns an error when the package directory exists but its package clause is
// unparseable or self-conflicting — see ParsePackageClause.
func PackageDataFromNames(names []string, projectDir string) ([]BootstrapPackageData, error) {
	var pkgs []BootstrapPackageData
	for _, name := range names {
		// Nested paths use forward slashes; flat names work the same way (no
		// slash → leaf == importPath).
		importPath := strings.TrimPrefix(filepath.ToSlash(name), "/")
		// The Go package identifier is the leaf's package clause when the
		// directory exists; the canonical snake_case form otherwise.
		leaf := importPath
		if idx := strings.LastIndex(leaf, "/"); idx >= 0 {
			leaf = leaf[idx+1:]
		}
		leaf = naming.GoPackage(leaf)
		if projectDir != "" {
			dir := filepath.Join(projectDir, "internal", filepath.FromSlash(importPath))
			if fi, statErr := os.Stat(dir); statErr == nil && fi.IsDir() {
				pkgName, perr := ParsePackageClause(dir)
				if perr != nil {
					return nil, fmt.Errorf("resolving internal package %q: %w", name, perr)
				}
				leaf = pkgName
			}
		}
		// FieldName encodes the full path (PascalCase concatenation) so nested
		// packages with the same leaf name don't share an exported struct
		// field. ToPascalCase already treats '/' as a separator-ish via the
		// underscore replacement we apply first.
		fieldNameSrc := strings.ReplaceAll(importPath, "/", "_")
		fieldName := naming.ToExportedFieldName(naming.GoPackage(fieldNameSrc))
		if strings.Contains(importPath, "/") {
			// For nested paths, ToExportedFieldName only uppercases the first
			// rune. ToPascalCase capitalizes every segment, which is what we
			// want when there are multiple path components.
			fieldName = naming.ToPascalCase(fieldNameSrc)
		}
		fallible := false
		ctor := DefaultConstructorName
		if projectDir != "" {
			pkgDir := filepath.Join(projectDir, "internal", filepath.FromSlash(importPath))
			fallible, _ = DetectFallibleConstructor(pkgDir)
			ctor = DetectConstructorName(pkgDir)
		}
		pkgs = append(pkgs, BootstrapPackageData{
			Name:        name,
			Package:     leaf,
			ImportPath:  importPath,
			FieldName:   fieldName,
			VarName:     lowerFirst(fieldName),
			Fallible:    fallible,
			Constructor: ctor,
			Alias:       leaf,
		})
	}
	return pkgs, nil
}
