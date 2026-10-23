// File: internal/cli/lint_config_deps.go
//
// config-deps — `forge lint --config-deps`.
//
// A scalar Deps field (string / int / bool / float / time.Duration /
// ...) is the naked-scalar antipattern: scalars are CONFIGURATION, not
// collaborators. wire_gen resolves Deps fields against App/AppExtras
// collaborator fields, so a scalar either regenerates as a typed zero
// + TODO forever (the kalshi-trader `WTIPersistMaxPerTick int`
// friction, fr-ad24278452) or forces the AppExtras + setup.go
// hand-projection boilerplate (their CycleInterval workaround).
//
// The supported shape is a component config block: declare a
// `message <Component>Config { ... }` in proto/<project>/config/v1/config.proto
// with (forge.v1.config) leaf annotations, compose it on AppConfig
// (`<Component>Config <component> = <tag>;`), and take the generated
// block as ONE typed Deps field (`Cfg *config.<Component>Config`).
// internal/app/compose.go fills the field from the loaded Config by TYPE
// (infra.Cfg.Get<Component>()), env binding / per-env deploy/kcl/<env>/config.k
// projection all flow from the same proto annotations, and validateDeps
// stops seeing phantom nil scalars. `forge scaffold adapter` writes this
// shape from birth.
//
// What it flags
//
// Every scalar-typed field on a conventional `Deps` struct under the
// role roots internal/<pkg>/, handlers/<svc>/, workers/<w>/,
// operators/<o>/. Scalar means: a predeclared scalar identifier
// (string, bool, int*, uint*, float*, complex*, byte, rune, uintptr)
// or time.Duration. Pointers-to-scalar are flagged too (`*int` is
// still configuration). Everything else — interfaces, structs, funcs,
// slices, maps — is a collaborator shape and is never flagged.
//
// Findings are severity "warning" and never gate the build: a scalar
// Deps field compiles and may even be hand-wired today. The lint is
// the nudge toward the config-block declaration; the wire_gen TODO
// hint carries the same remediation for the fields that are actually
// unresolved.
//
// Why this lives in cli/ rather than internal/linter/forgeconv/: same
// rationale as lint_optional_deps_guard.go — it's a Deps-shape
// companion sharing the collect/format split so `forge lint --json`
// and `forge project audit --json` reuse one engine.

package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// ConfigDepsFinding is the exported alias for the config-deps finding,
// consumed by the internal/cli/audit group's auditConfigDeps roll-up. A
// type alias (not a new type) so audit's field access
// (.Role/.Package/.File/.Line/.Field/.Type) is unchanged from when both
// commands lived in package cli.
type ConfigDepsFinding = configDepsFinding

// CollectConfigDepsFindings is the exported entry the audit group calls. It
// forwards to the package-internal collector; the audit roll-up is the only
// cross-package consumer.
func CollectConfigDepsFindings(projectDir string) ([]ConfigDepsFinding, error) {
	return collectConfigDepsFindings(projectDir)
}

// configDepsFinding is one scalar Deps field. File is
// projectDir-relative; Line/Col are 1-based positions of the field name.
type configDepsFinding struct {
	File    string
	Line    int
	Col     int
	Role    string // "internal" | "handlers" | "workers" | "operators"
	Package string // directory / package name, e.g. "trader"
	Field   string // the scalar Deps field, e.g. "WTIPersistMaxPerTick"
	Type    string // pretty-printed field type, e.g. "int"
}

// configDepsFixHint renders the canonical remediation — shown in text
// mode and carried as fix_hint in JSON. The snippet is paste-ready: the
// proto block message, the AppConfig composition line, and the typed
// Deps field replacing the scalar.
func configDepsFixHint(f configDepsFinding) string {
	block := naming.ToPascalCase(f.Package) + "Config"
	protoField := naming.ToSnakeCase(f.Field)
	return fmt.Sprintf(
		"scalar Deps fields are configuration — declare a config block in proto/<project>/config/v1/config.proto: `message %s { %s %s = 1 [(forge.v1.config) = {env_var: %q, description: \"...\"}]; }`, compose it on AppConfig (`%s %s = <next tag>;`), run `forge generate`, and replace `%s %s` with `Cfg *config.%s` (internal/app/compose.go fills it from the loaded config by type; per-env values go in deploy/kcl/<env>/config.k as `%s = <value>`)",
		block, protoScalarType(f.Type), protoField,
		strings.ToUpper(naming.ToSnakeCase(f.Package))+"_"+strings.ToUpper(protoField),
		block, naming.ToSnakeCase(f.Package),
		f.Field, f.Type, block, protoField)
}

// protoScalarType maps a Go scalar type to the closest proto scalar for
// the fix-hint snippet. Durations and unmapped scalars fall back to
// string (the AppConfig convention for Go-duration fields like
// PRE_STOP_DELAY).
func protoScalarType(goType string) string {
	switch strings.TrimPrefix(goType, "*") {
	case "int32", "uint32", "int16", "int8", "uint16", "uint8", "byte", "rune":
		return "int32"
	case "int", "int64", "uint", "uint64", "uintptr":
		return "int64"
	case "bool":
		return "bool"
	case "float32":
		return "float"
	case "float64":
		return "double"
	default:
		return "string" // string, time.Duration ("5s"), anything exotic
	}
}

// collectConfigDepsFindings is the shared engine behind text mode,
// `forge lint --json`, and `forge project audit --json`. Findings come back
// sorted by (file, line, col) so output is deterministic.
func collectConfigDepsFindings(projectDir string) ([]configDepsFinding, error) {
	var findings []configDepsFinding

	// Same role roots as the sibling Deps-shape lints. Missing roots are
	// fine — many projects ship no operators/ or workers/.
	roleRoots := []string{"internal", "internal/handlers", "internal/workers", "internal/operators"}
	for _, role := range roleRoots {
		rootDir := filepath.Join(projectDir, role)
		entries, err := os.ReadDir(rootDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", rootDir, err)
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name() == "testdata" {
				continue
			}
			pkgFindings, scanErr := scanPackageForScalarDeps(projectDir, filepath.Join(rootDir, e.Name()), role, e.Name())
			if scanErr != nil {
				return nil, scanErr
			}
			findings = append(findings, pkgFindings...)
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Col < findings[j].Col
	})
	return findings, nil
}

// scanPackageForScalarDeps parses every non-test, non-generated .go
// file in pkgDir looking for the package-level `Deps` struct and flags
// its scalar-typed fields.
func scanPackageForScalarDeps(projectDir, pkgDir, role, pkgName string) ([]configDepsFinding, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pkgDir, err)
	}

	var findings []configDepsFinding
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		fp := filepath.Join(pkgDir, name)
		file, parseErr := parser.ParseFile(fset, fp, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			// Don't double-report parse errors — the Go toolchain will.
			continue
		}
		rel, relErr := filepath.Rel(projectDir, fp)
		if relErr != nil {
			rel = fp
		}

		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "Deps" {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				for _, fld := range st.Fields.List {
					typeStr, scalar := scalarDepsType(fld.Type)
					if !scalar {
						continue
					}
					for _, n := range fld.Names {
						if !n.IsExported() {
							continue
						}
						pos := fset.Position(n.Pos())
						findings = append(findings, configDepsFinding{
							File:    rel,
							Line:    pos.Line,
							Col:     pos.Column,
							Role:    role,
							Package: pkgName,
							Field:   n.Name,
							Type:    typeStr,
						})
					}
				}
			}
		}
	}
	return findings, nil
}

// scalarDepsType reports whether a Deps field type expression denotes a
// configuration scalar, returning its pretty-printed form. Recognized:
// predeclared scalar identifiers, time.Duration, and pointers to
// either. Everything else (interfaces, structs, funcs, slices, maps,
// other selector types) is a collaborator shape.
func scalarDepsType(t ast.Expr) (string, bool) {
	if star, ok := t.(*ast.StarExpr); ok {
		inner, scalar := scalarDepsType(star.X)
		if !scalar {
			return "", false
		}
		return "*" + inner, true
	}
	switch v := t.(type) {
	case *ast.Ident:
		switch v.Name {
		case "string", "bool",
			"int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64",
			"uintptr", "byte", "rune",
			"float32", "float64",
			"complex64", "complex128":
			return v.Name, true
		}
	case *ast.SelectorExpr:
		if x, ok := v.X.(*ast.Ident); ok && x.Name == "time" && v.Sel.Name == "Duration" {
			return "time.Duration", true
		}
	}
	return "", false
}
