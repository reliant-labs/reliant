package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/templates"
)

// ServiceFileSymbols are the top-level names a handler package's service.go
// declares (service/service.go.tmpl): the Deps struct, the Service type and
// its constructor. Everything else in the file hangs off them.
var ServiceFileSymbols = []string{"Deps", "Service", "New"}

// HandlerPackageDecls reads the non-test Go files of a handler directory and
// returns their package clause ("" when there are none) and, for each of
// names the package declares at top level, the file that declares it.
//
// A file that does not parse is skipped rather than failing the read: the
// question is "would writing service.go redeclare something", and a file the
// compiler will reject anyway is reported by the build, not here.
func HandlerPackageDecls(dir string, names ...string) (pkg string, declared map[string]string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	declared = map[string]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		if pkg == "" {
			pkg = f.Name.Name
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && want[d.Name.Name] {
					declared[d.Name.Name] = name
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && want[ts.Name.Name] {
						declared[ts.Name.Name] = name
					}
				}
			}
		}
	}
	return pkg, declared, nil
}

// DescribeDecls renders declared (HandlerPackageDecls) as "Service in
// handler.go, New in handler.go", sorted, for a message.
func DescribeDecls(declared map[string]string) string {
	parts := make([]string, 0, len(declared))
	for n, f := range declared {
		parts = append(parts, n+" in "+f)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// EnsureServiceFile writes service.go into an EXISTING handler directory that
// has none — a directory someone started by hand (an rpc file, a helper)
// before the service was scaffolded. Without it the package's rpc methods
// hang off a *Service nothing declares, and compose.go calls a New(Deps) that
// does not exist, so the build fails with "undefined: Service".
//
// It writes only a file that is missing, never over one, and only when the
// package declares none of ServiceFileSymbols elsewhere (a hand-written
// Service in handler.go would be redeclared). A service.go the project
// deleted or declined (`forge project disown --absent`) is not re-born:
// writeUserScaffoldIfAbsent consults the scaffold ledger. The package clause
// is the directory's own, so it agrees with the files already there.
//
// It reports whether it wrote the file and, when it did not because the
// package already declares part of it, what it found.
func EnsureServiceFile(svc ServiceDef, projectDir, targetDir string) (wrote bool, found map[string]string, err error) {
	path := filepath.Join(targetDir, "service.go")
	if _, err := os.Stat(path); err == nil {
		return false, nil, nil
	} else if !os.IsNotExist(err) {
		return false, nil, err
	}
	pkg, declared, err := HandlerPackageDecls(targetDir, ServiceFileSymbols...)
	if err != nil {
		return false, nil, err
	}
	if len(declared) > 0 {
		return false, declared, nil
	}
	data := mapServiceDefToTemplateData(svc, projectDir)
	if pkg != "" {
		data.ServicePackage = pkg
	}
	content, err := templates.ServiceTemplates().Render("service.go.tmpl", data)
	if err != nil {
		return false, nil, fmt.Errorf("render service.go.tmpl: %w", err)
	}
	wrote, err = writeUserScaffoldIfAbsent(path, content)
	return wrote, nil, err
}
