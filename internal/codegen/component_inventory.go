package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// Inventory is a project's complete component set — Connect servers,
// workers, crons, operators and secondary binaries — read from the sources
// that DECLARE them. Code lives in code: there is no manifest and no
// forge.yaml block, so each kind is read from the artifact that IS its
// declaration.
//
//	server   → the proto descriptor            (IntrospectComponents)
//	worker   → internal/workers/<pkg>/         (DiscoverWorkerSpecs)
//	cron     → the same, carrying a Schedule const
//	operator → internal/operators/<pkg>/       (DiscoverOperatorSpecs)
//	          + APIGroup/APIVersion consts in the operator package
//	          + <lower>_controller.go per CRD
//	binary   → cmd/<pkg>/main.go, minus the primary binary
//
// An Inventory is only ever produced by [DiscoverProjectComponents]. Nothing
// hands one out as a struct field, so "I have not looked yet" is not
// representable: a caller that wants to know what the project contains has to
// go and read the project.
//
// Resolve components through the methods below rather than re-scanning the
// slice, so every consumer shares one matching rule (exact name; kind
// compared through EffectiveKind).
type Inventory []config.ComponentConfig

// Named returns the component claiming name, and whether one does.
// Components share a single name namespace across every kind.
func (inv Inventory) Named(name string) (config.ComponentConfig, bool) {
	for _, c := range inv {
		if c.Name == name {
			return c, true
		}
	}
	return config.ComponentConfig{}, false
}

// OfKind returns the components whose EffectiveKind is kind, in inventory
// order.
func (inv Inventory) OfKind(kind string) Inventory {
	var out Inventory
	for _, c := range inv {
		if c.EffectiveKind() == kind {
			out = append(out, c)
		}
	}
	return out
}

// Names returns every component name, in inventory order.
func (inv Inventory) Names() []string {
	out := make([]string, 0, len(inv))
	for _, c := range inv {
		out = append(out, c.Name)
	}
	return out
}

// OperatorAPIGroupConst / OperatorAPIVersionConst are the constant names the
// operator scaffold emits to record the operator's CRD API coordinates in its
// own package. `forge scaffold crd` reads them to default a new CRD's
// group/version to its parent operator's.
const (
	OperatorAPIGroupConst   = "APIGroup"
	OperatorAPIVersionConst = "APIVersion"
)

// DiscoverProjectComponents reads the project at projectDir and returns its
// [Inventory]. primaryBinary is the cmd/<bin>/ leaf that hosts the project's
// main command tree (the forge.yaml project name); it is excluded from the
// binary-kind rows because it is the server binary, not an `scaffold binary`
// component.
//
// ABSENCE IS AN ANSWER, and it is the only reason the result is ever empty:
// a project with no proto descriptor yet, no internal/workers/ and no
// secondary cmd/ trees genuinely has no components. There is no error return
// because there is no source whose failure would be indistinguishable from
// that — every input is a directory or descriptor that is either there and
// readable or legitimately not there yet.
func DiscoverProjectComponents(projectDir, primaryBinary string) Inventory {
	var out Inventory
	out = append(out, IntrospectComponents(projectDir)...)

	for _, spec := range DiscoverWorkerSpecs(projectDir) {
		kind, schedule := workerKindFor(filepath.Join(projectDir, "internal", "workers", spec.Name))
		out = append(out, config.ComponentConfig{
			Name:     spec.Name,
			Kind:     kind,
			Path:     "internal/workers/" + spec.Name,
			Schedule: schedule,
		})
	}

	for _, spec := range DiscoverOperatorSpecs(projectDir) {
		dir := filepath.Join(projectDir, "internal", "operators", spec.Name)
		group, version := operatorAPICoordinates(dir)
		out = append(out, config.ComponentConfig{
			Name:    spec.Name,
			Kind:    config.ComponentKindOperator,
			Path:    "internal/operators/" + spec.Name,
			Group:   group,
			Version: version,
			CRDs:    discoverOperatorCRDs(dir, group, version),
		})
	}

	for _, name := range discoverSecondaryBinaries(projectDir, primaryBinary) {
		out = append(out, config.ComponentConfig{
			Name: name,
			Kind: config.ComponentKindBinary,
			Path: "cmd/" + name,
		})
	}
	return out
}

// workerKindFor distinguishes a cron worker from a long-running one by the
// Schedule constant the cron template emits — the same "the source IS the
// declaration" rule the rest of discovery follows — and returns the cron
// expression along with it, because that constant IS the schedule. A worker
// package with no Schedule const is long-running; its schedule is empty.
func workerKindFor(dir string) (kind, schedule string) {
	if s, ok := parsePackageStringConst(dir, "Schedule"); ok {
		return config.ComponentKindCron, s
	}
	return config.ComponentKindWorker, ""
}

// operatorAPICoordinates reads the operator package's APIGroup / APIVersion
// constants. Both are empty for an operator scaffolded before those constants
// existed, which leaves the caller on its documented default (the project's
// `<name>.io` group and v1alpha1).
func operatorAPICoordinates(dir string) (group, version string) {
	group, _ = parsePackageStringConst(dir, OperatorAPIGroupConst)
	version, _ = parsePackageStringConst(dir, OperatorAPIVersionConst)
	return group, version
}

// discoverOperatorCRDs lists the CRDs an operator reconciles from the
// <lower>_controller.go shims `forge scaffold crd` writes into its package.
// The file IS the declaration — the same rule WebhookNamesForService follows.
func discoverOperatorCRDs(dir, group, version string) []config.CRDConfig {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []config.CRDConfig
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, "_controller.go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		lower := strings.TrimSuffix(n, "_controller.go")
		if lower == "" {
			continue
		}
		out = append(out, config.CRDConfig{
			Name:    naming.ToPascalCase(lower),
			Group:   group,
			Version: version,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// discoverSecondaryBinaries lists the cmd/<name>/ trees that carry a main.go,
// excluding the project's primary binary. `forge scaffold binary` writes
// exactly that shape (cmd/<pkg>/main.go + internal/<pkg>/), so the tree is the
// declaration.
func discoverSecondaryBinaries(projectDir, primaryBinary string) []string {
	entries, err := os.ReadDir(filepath.Join(projectDir, "cmd"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == primaryBinary {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(projectDir, "cmd", e.Name(), "main.go")); statErr != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// parsePackageStringConst returns the value of a package-level untyped string
// constant declared anywhere in dir's non-test Go files. AST-based, so a
// mention of the name in a comment or a string body never matches.
func parsePackageStringConst(dir, name string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range vs.Names {
					if ident.Name != name || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, uerr := strconv.Unquote(lit.Value)
					if uerr != nil {
						continue
					}
					return v, true
				}
			}
		}
	}
	return "", false
}
