package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// configBlocks is what compose.go needs to fill a component's config-block
// Deps field: the component config blocks the loaded Config composes, keyed
// by block type, each with the getter that returns it.
//
// A scalar Deps field is configuration with no type to wire it by, so it can
// only ever be resolved to a typed zero and a TODO (forge-config-deps). The
// shape that lint asks for instead is a config block — a message of
// annotated leaves in proto/<project>/config/v1/config.proto, composed on AppConfig —
// taken whole as `Cfg *config.<Block>`. That field DOES have a type, and the
// loaded Config holds exactly one value of it, so compose.go resolves it the
// way it resolves every other dependency: by type, to `infra.Cfg.Get<Field>()`.
//
// It is read from the generated Go rather than from the proto: pkg/config's
// `type Config = configv1.<Root>` names the root message, and that message's
// generated struct names each block field and its type. That is the code
// compose.go has to compile against, so it cannot disagree with it.
type configBlocks struct {
	// pkgPaths are the import paths whose types name the blocks: pkg/config,
	// which re-exports each block under its own name, and the generated
	// proto package itself.
	pkgPaths map[string]bool
	// getters maps a block's type name to the root getter(s) returning it.
	// A type composed twice on the root is ambiguous and resolves to
	// neither field.
	getters map[string][]string
}

// parseConfigBlocks reads the config blocks the project's loaded Config
// composes. A project with no generated pkg/config, or whose Config is not an
// alias of a generated message, has none: every config-block Deps field then
// falls through to the ordinary missing-provider path.
func parseConfigBlocks(projectDir, modulePath string) configBlocks {
	out := configBlocks{getters: map[string][]string{}}
	if modulePath == "" {
		return out
	}
	cfgPath := modulePath + "/pkg/config"
	genPath, root := configAliasTarget(filepath.Join(projectDir, "pkg", "config"))
	rel, ok := strings.CutPrefix(genPath, modulePath+"/")
	if root == "" || !ok {
		return out
	}
	out.pkgPaths = map[string]bool{cfgPath: true, genPath: true}

	genDir := filepath.Join(projectDir, filepath.FromSlash(rel))
	entries, err := os.ReadDir(genDir)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(genDir, e.Name()), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		st := findStructType(file, root)
		if st == nil {
			continue
		}
		for _, f := range st.Fields.List {
			// A singular message field is `*<Msg>`, unqualified because the
			// message is generated into the same package. Well-known types
			// (`*durationpb.Duration`) are qualified, and enums and oneof
			// wrappers are not pointers, so none of them can match.
			star, ok := f.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok {
				continue
			}
			for _, n := range f.Names {
				if n.IsExported() {
					out.getters[ident.Name] = append(out.getters[ident.Name], "Get"+n.Name)
				}
			}
		}
		return out
	}
	return out
}

// resolve returns the compose expression for a Deps field of type depType —
// `infra.Cfg.Get<Field>()` — when depType is a pointer to a config block the
// root composes exactly once. imports is the consuming package's alias →
// import path map, which proves the qualifier names the project's config
// rather than some other package's same-named type.
func (b configBlocks) resolve(depType string, imports map[string]string) (string, bool) {
	if len(b.getters) == 0 {
		return "", false
	}
	qualified, ok := strings.CutPrefix(depType, "*")
	if !ok {
		return "", false
	}
	alias, name, ok := strings.Cut(qualified, ".")
	if !ok || strings.Contains(name, ".") {
		return "", false
	}
	if !b.pkgPaths[imports[alias]] {
		return "", false
	}
	getters := b.getters[name]
	if len(getters) != 1 {
		return "", false
	}
	return "infra.Cfg." + getters[0] + "()", true
}

// configAliasTarget reads pkg/config for `type Config = <alias>.<Msg>` and
// returns the import path <alias> names and <Msg>, or "" for both when the
// package declares no such alias.
func configAliasTarget(dir string) (importPath, message string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
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
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "Config" || !ts.Assign.IsValid() {
					continue
				}
				sel, ok := ts.Type.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					continue
				}
				for _, imp := range file.Imports {
					p, perr := importPathFromSpec(imp)
					if perr == nil && aliasForImport(imp, p) == pkg.Name {
						return p, sel.Sel.Name
					}
				}
			}
		}
	}
	return "", ""
}

// findStructType returns the struct type declared as name in file, or nil.
func findStructType(file *ast.File, name string) *ast.StructType {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				continue
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				return st
			}
		}
	}
	return nil
}
