package audit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/codegen"
)

// unhandledRPC is one rpc of a managed service whose handler package declares
// no *Service method for it.
type unhandledRPC struct {
	Service string `json:"service"`
	RPC     string `json:"rpc"`
	Dir     string `json:"dir"`
}

// auditUnhandledRPCs reports every rpc of a MANAGED service that has no
// `*Service` method in its handler package.
//
// A scaffolded Service embeds the Connect Unimplemented<Svc>Handler, so such
// an rpc compiles and serves CodeUnimplemented — with no stub in the tree
// saying so, nothing in `forge project shapes`, and nothing in orphan_stubs,
// which only sees methods that exist. Every path forge owns gives an rpc a
// method: a pb-through stub for a custom rpc, a delegating shim for a CRUD
// one. So a missing one means those paths disagreed (the `//forge:no-orm`
// gap: the stub step skipped a CRUD-named rpc the CRUD step never ran for),
// generate has not run since the proto changed, or a hand edit deleted it —
// a lane porting handlers that forgets one method ships it as a 501.
//
// Warn, not error: a project scaffolded before webhook-only services lost
// their placeholder rpcs legitimately carries some, and those are better
// deleted from the proto than gated on.
//
// A Service struct that embeds anything besides the Unimplemented handler may
// get methods by promotion this scan cannot see; such a service is listed
// under `unresolved_services` instead of being guessed at.
func auditUnhandledRPCs(f *factory.Factory, projectDir string) audittype.Category {
	ok := audittype.Category{Status: audittype.StatusOK, Summary: "every rpc of every managed service has a handler method"}
	if !f.Audit.ProjectDefinesConnectServices(projectDir) {
		return audittype.Category{Status: audittype.StatusOK, Summary: "no Connect services (n/a)"}
	}
	declared, err := codegen.ParseDeclaredServices(projectDir)
	if err != nil {
		return audittype.Category{Status: audittype.StatusWarn, Summary: "could not read the service descriptor: " + err.Error()}
	}
	var missing []unhandledRPC
	var unresolved []string
	for _, svc := range codegen.ManagedServices(declared) {
		if len(svc.Methods) == 0 {
			continue
		}
		res, rerr := codegen.ResolveServiceComponent(projectDir, svc.Name)
		if rerr != nil {
			unresolved = append(unresolved, svc.Name)
			continue
		}
		rel := "internal/handlers/" + res.ImportLeaf
		methods, resolvable := serviceMethodSet(res.Dir)
		if !resolvable {
			unresolved = append(unresolved, svc.Name)
			continue
		}
		for _, m := range svc.Methods {
			if !methods[m.GoName()] {
				missing = append(missing, unhandledRPC{Service: svc.Name, RPC: m.Name, Dir: rel})
			}
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Service != missing[j].Service {
			return missing[i].Service < missing[j].Service
		}
		return missing[i].RPC < missing[j].RPC
	})
	sort.Strings(unresolved)

	details := map[string]any{
		"unhandled_rpcs": missing,
		"hint": fmt.Sprintf("run `%s generate`: a custom rpc (including one marked `// %s`) gets a pb-through stub and a CRUD rpc a delegating shim; "+
			"an rpc that should not exist is deleted from the proto", cmdutil.Name(), codegen.ProtoMarkerCustom),
	}
	if len(unresolved) > 0 {
		details["unresolved_services"] = unresolved
	}
	if len(missing) == 0 {
		ok.Details = details
		return ok
	}
	names := make([]string, 0, len(missing))
	for _, m := range missing {
		names = append(names, m.Service+"."+m.RPC)
	}
	if len(names) > 6 {
		names = append(names[:6], fmt.Sprintf("… %d more", len(missing)-6))
	}
	return audittype.Category{
		Status: audittype.StatusWarn,
		Summary: fmt.Sprintf("%d rpc(s) of managed services have no handler method and serve CodeUnimplemented with no stub saying so: %s",
			len(missing), strings.Join(names, ", ")),
		Details: details,
	}
}

// serviceMethodSet returns the methods declared on Service in the handler
// package at dir (non-test files), and whether that set is the whole answer.
// It is not when Service embeds a type other than the Connect
// Unimplemented…Handler, whose methods Go may promote. A missing dir is an
// empty, resolvable set: the service was never scaffolded.
func serviceMethodSet(dir string) (map[string]bool, bool) {
	methods := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return methods, os.IsNotExist(err)
	}
	fset := token.NewFileSet()
	resolvable := true
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			return methods, false // a broken file may hold the method
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil && len(d.Recv.List) > 0 && receiverTypeName(d.Recv.List[0].Type) == "Service" {
					methods[d.Name.Name] = true
				}
			case *ast.GenDecl:
				if embedsBeyondUnimplemented(d) {
					resolvable = false
				}
			}
		}
	}
	return methods, resolvable
}

// embedsBeyondUnimplemented reports whether decl declares `type Service
// struct` with an embedded field other than a Connect Unimplemented…Handler.
func embedsBeyondUnimplemented(decl *ast.GenDecl) bool {
	if decl.Tok != token.TYPE {
		return false
	}
	for _, spec := range decl.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Service" {
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			continue
		}
		for _, field := range st.Fields.List {
			if len(field.Names) > 0 {
				continue
			}
			typ := field.Type
			if star, isStar := typ.(*ast.StarExpr); isStar {
				typ = star.X
			}
			name := ""
			switch t := typ.(type) {
			case *ast.Ident:
				name = t.Name
			case *ast.SelectorExpr:
				name = t.Sel.Name
			}
			if !strings.HasPrefix(name, "Unimplemented") || !strings.HasSuffix(name, "Handler") {
				return true
			}
		}
	}
	return false
}
