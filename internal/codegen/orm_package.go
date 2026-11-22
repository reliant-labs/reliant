package codegen

import (
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"
)

// ProjectORMPackage is the ORM placement projectDir's forge.yaml declares
// (database.orm.package), the default internal/db when it declares none or
// cannot be read. Best-effort by design, like projectAPIRESTEnabled: an
// unreadable forge.yaml has already been reported by the load that matters.
func ProjectORMPackage(projectDir string) config.ORMConfig {
	cfg, err := config.LoadProjectDir(projectDir)
	if err != nil {
		return config.ORMConfig{}
	}
	return config.ORMPackage(cfg)
}

// ORMImportSpec is the generated ORM package as an import spec for code
// that qualifies it as `db.`: the import path, or "db <path>" when the
// package is not called db. The spec format ("[alias ]path") is the one
// crudShimImports and ensureImportLine already speak.
//
// Generated CRUD code no longer assumes `db.`: it binds the ORM with
// ChooseORMBinding, which avoids the names the file already binds.
func ORMImportSpec(modulePath string, orm config.ORMConfig) string {
	path := modulePath + "/" + orm.PackageDir()
	if orm.PackageName() == "db" {
		return path
	}
	return "db " + path
}

// ORMBinding is how one Go file imports the generated ORM package and
// qualifies its identifiers.
type ORMBinding struct {
	// Spec is the import in ensureImportLine's form: the path, or
	// "alias path" when Qual is not the package's own name.
	Spec string
	// Qual is the identifier the file's code qualifies the ORM with.
	Qual string
}

// ormQualFallback is the alias the ORM is imported under when neither its
// own name nor the legacy db is free in a file; ormstore2, ormstore3, …
// follow it.
const ormQualFallback = "ormstore"

// ormQualReserved are the identifiers the generated CRUD bodies declare as
// locals and parameters (handlers_crud_ops_gen.go.tmpl,
// handlers_crud_shim_method.go.tmpl). A package qualifier spelled like one
// would be shadowed inside the body that uses it.
var ormQualReserved = map[string]bool{
	"build": true, "c": true, "claims": true, "ctx": true, "del": true, "e": true,
	"entity": true, "err": true, "fetch": true, "fields": true, "filters": true,
	"id": true, "it": true, "items": true, "m": true, "nextPageToken": true,
	"op": true, "opts": true, "out": true, "owner": true, "persist": true,
	"persistMasked": true, "r": true, "req": true, "resp": true, "row": true,
	"rows": true, "s": true, "totalCount": true,
}

// ChooseORMBinding binds the ORM package in a file where taken reports
// the identifiers already bound: the file's other imports and the
// package's top-level declarations. The first free name of the ORM's own
// package name, the legacy db alias, then ormstore (ormstore2, …) wins, so
// a default project (package db, nothing else named db) is imported
// unaliased and qualified as db. exactly as before, and a relocated ORM
// (internal/store) is imported unaliased and qualified as store. — never as
// a db that a project's own internal/db already holds.
func ChooseORMBinding(modulePath string, orm config.ORMConfig, taken func(string) bool) ORMBinding {
	importPath := modulePath + "/" + orm.PackageDir()
	free := func(name string) bool { return !taken(name) && !ormQualReserved[name] }
	name := orm.PackageName()
	if free(name) {
		return ORMBinding{Spec: importPath, Qual: name}
	}
	alias := ""
	for _, cand := range []string{"db", ormQualFallback} {
		if free(cand) {
			alias = cand
			break
		}
	}
	for n := 2; alias == ""; n++ {
		if cand := ormQualFallback + strconv.Itoa(n); free(cand) {
			alias = cand
		}
	}
	return ORMBinding{Spec: alias + " " + importPath, Qual: alias}
}

// importSpecName is the identifier an import spec ("path" or "alias path")
// binds. An unrenamed import is assumed to bind its path's last element,
// minus a /vN major-version suffix (fileImportSpecs' rule).
func importSpecName(spec string) string {
	if alias, _, ok := strings.Cut(spec, " "); ok {
		return alias
	}
	base := path.Base(spec)
	if len(base) > 1 && base[0] == 'v' && strings.Trim(base[1:], "0123456789") == "" {
		base = path.Base(path.Dir(spec))
	}
	return base
}

// importSpecPath is the path of an import spec ("path" or "alias path").
func importSpecPath(spec string) string {
	if _, p, ok := strings.Cut(spec, " "); ok {
		return p
	}
	return spec
}

// importBinding retains each path and its literal alias, including _ and .;
// name is empty for an unaliased import until its package name is resolved.
type importBinding struct{ name, path string }

func shimImports(src []byte) ([]importBinding, error) {
	if len(src) == 0 {
		return nil, nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []importBinding
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		b := importBinding{path: p}
		if imp.Name != nil {
			b.name = imp.Name.Name
		}
		out = append(out, b)
	}
	return out, nil
}

// shimPackageNames resolves declarations without type-checking the user's
// handlers. Standard and local packages are read from disk; remaining paths
// are resolved in one name-only Go toolchain query (replaces, workspaces, etc.).
func shimPackageNames(projectDir, module string, paths []string, generated map[string]string) map[string]string {
	names := map[string]string{}
	var load []string
	for _, p := range paths {
		var dir string
		if rel, ok := strings.CutPrefix(p, module+"/"); ok {
			dir = filepath.Join(projectDir, filepath.FromSlash(rel))
		} else if first, _, _ := strings.Cut(p, "/"); !strings.Contains(first, ".") {
			dir = filepath.Join(build.Default.GOROOT, "src", filepath.FromSlash(p))
		}
		if dir != "" {
			if pkg, _ := build.Default.ImportDir(dir, 0); pkg != nil && pkg.Name != "" {
				names[p] = pkg.Name
				continue
			}
			if generated[p] != "" {
				continue // Forge-local files may not be written yet.
			}
		}
		load = append(load, p)
	}
	if len(load) != 0 {
		pkgs, _ := packages.Load(&packages.Config{Mode: packages.NeedName, Dir: projectDir, Env: goexec.Env()}, load...)
		for _, pkg := range pkgs {
			if pkg.Name != "" {
				names[pkg.PkgPath] = pkg.Name
			}
		}
	}
	return names
}

// generatedShimImportNames are the imports whose package clauses Forge owns
// or whose names are part of the runtime contract, including when the target
// files have not been written yet in this generation run.
func generatedShimImportNames(data CRUDTemplateData) map[string]string {
	return map[string]string{
		importSpecPath(data.DBImport):              data.orm.PackageName(),
		data.Module + "/internal/metrics":          "metrics",
		data.Module + "/pkg/" + crudAuthSeamPkg:    crudAuthSeamPkg,
		"connectrpc.com/connect":                   "connect",
		crudLibImport:                              "crud",
		"github.com/reliant-labs/forge/pkg/orm":    "orm",
		"github.com/reliant-labs/forge/pkg/svcerr": "svcerr",
	}
}

// opsFileImportNames are the names handlers_crud_ops_gen.go.tmpl's other
// imports bind; the ORM's qualifier in that file must be none of them.
var opsFileImportNames = map[string]bool{
	"context": true, "crud": true, "fmt": true, "orm": true, "pb": true,
	"time": true, "timestamppb": true,
}

// shimORMBinding binds the ORM package in the owned handlers_crud.go that
// methods are about to be written into: existing is the file's current
// bytes (nil at its birth). A name the file already binds the ORM's path
// to is reused, so shims appended to a file scaffolded under an older
// forge (`db "<module>/internal/store"`) match the ones beside them, and
// a file that imports the store itself is never given a second import.
// Otherwise the name is chosen free of the file's imports, the imports
// the new shims add, and the package's top-level declarations. The
// file's existing lines are never rewritten.
func shimORMBinding(projectDir string, data CRUDTemplateData, methods []CRUDMethodTemplateData, existing []byte) (ORMBinding, error) {
	ormPath := importSpecPath(data.DBImport)
	wanted := crudShimImports(CRUDTemplateData{
		Module: data.Module, ProtoPackage: data.ProtoPackage, DBImport: data.DBImport, ConnectV2: data.ConnectV2, CRUDMethods: methods,
	})
	needsORM := false
	for _, imp := range wanted {
		if importSpecPath(imp) == ormPath {
			needsORM = true
			break
		}
	}
	if !needsORM {
		return ORMBinding{Spec: data.DBImport, Qual: data.DBQual}, nil
	}
	bound, err := shimImports(existing)
	if err != nil {
		return ORMBinding{}, fmt.Errorf("parse imports before appending ORM shims: %w", err)
	}
	known := generatedShimImportNames(data)
	for _, b := range bound {
		if b.path != ormPath || b.name == "." || b.name == "_" {
			continue
		}
		name := b.name
		if name == "" {
			name = known[ormPath]
		}
		if ormQualReserved[name] {
			return ORMBinding{}, fmt.Errorf("ORM import %q uses %q, shadowed by generated shim locals; give it a different explicit alias", ormPath, name)
		}
		// ensureImportLine preserves an existing import verbatim. Do not
		// choose a replacement qualifier that this import would never bind.
		if data.pkgDecls[name] {
			return ORMBinding{}, fmt.Errorf("ORM import %q uses %q, conflicting with a package declaration; give it a different explicit alias before appending CRUD shims", ormPath, name)
		}
		return ORMBinding{Spec: ormPath, Qual: name}, nil
	}
	var unknown []string
	var dotImport bool
	for _, b := range bound {
		if b.path == ormPath {
			return ORMBinding{}, fmt.Errorf("ORM import %s %q does not bind a qualifier; give it an explicit alias before appending CRUD shims", b.name, ormPath)
		}
		dotImport = dotImport || b.name == "."
		if b.name == "" && (known[b.path] == "" || strings.HasPrefix(b.path, data.Module+"/")) {
			unknown = append(unknown, b.path)
		}
	}
	resolved := shimPackageNames(projectDir, data.Module, unknown, known)
	taken := map[string]bool{}
	for _, b := range bound {
		if b.name == "_" || b.name == "." {
			continue
		}
		name := b.name
		if name == "" {
			name = resolved[b.path]
		}
		if name == "" {
			name = known[b.path]
		}
		if name == "" {
			return ORMBinding{}, fmt.Errorf("cannot resolve the package name of import %q; give it an explicit alias or make the dependency available before appending CRUD shims", b.path)
		}
		taken[name] = true
	}
	for _, imp := range wanted {
		p := importSpecPath(imp)
		if p != ormPath {
			if alias, _, explicit := strings.Cut(imp, " "); explicit {
				taken[alias] = true
			} else if name := known[p]; name != "" {
				taken[name] = true
			} else if name := resolved[p]; name != "" {
				taken[name] = true
			} else {
				// Owner evidence from an unaliased import uses the same
				// syntactic name as the emitted resolver call.
				taken[importSpecName(imp)] = true
			}
		}
	}
	return ChooseORMBinding(data.Module, data.orm, func(name string) bool {
		// Dot imports expose only exported identifiers, so a lower-case
		// alias remains safe without loading every imported symbol.
		return taken[name] || data.pkgDecls[name] || (dotImport && token.IsExported(name))
	}), nil
}
