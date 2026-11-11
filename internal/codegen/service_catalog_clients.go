package codegen

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

func catalogServiceName(s routesServiceView, services []ServiceDef) string {
	for _, d := range services {
		if d.Name == s.ProtoServiceName {
			return d.Package + "." + d.Name
		}
	}
	return s.ProtoServiceName
}

// graphCatalogClients recognizes the actual typed constructor marker in owned
// providers. No scan for New*Client names or endpoint string values is used.
func graphCatalogClients(in GraphGenInput, m *graphModel, diagnostics *[]servicecatalog.Diagnostic) []servicecatalog.Client {
	out := []servicecatalog.Client{}
	configFields := catalogConfigFields(in.ProjectDir)
	p := m.u.app
	if p == nil || p.TypesInfo == nil {
		return out
	}
	for _, file := range p.Syntax {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			provider := strings.TrimPrefix(fd.Name.Name, "New")
			if fd.Recv != nil {
				provider = strings.TrimPrefix(fd.Name.Name, "new")
			}
			if m.byName[provider] == nil {
				continue
			}
			// A registration is located by the provider func that makes it:
			// the call's own line would make the committed catalog move with
			// every edit above it (catalogSource).
			source := servicecatalog.Source{}
			if path := catalogPath(in.ProjectDir, p.Fset, fd.Pos()); path != "" {
				if sym := catalogFuncSymbol(fd); sym != "" {
					source = servicecatalog.Source{Path: path, Symbol: sym}
				}
			}
			calls := 0
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isCatalogNewClient(p.TypesInfo, call) {
					return true
				}
				calls++
				cl := catalogClient(p.TypesInfo, call)
				cl.Provider, cl.Source, cl.Resolution = provider, source, "unresolved"
				// Only proto identities, never arbitrary URLs or secret-like strings.
				if !catalogProtoRef(cl.Service) || !catalogProtoRef(cl.ConfigField) || cl.Constructor == "" || !configFields[cl.ConfigField] || !catalogClientService(p.TypesInfo, call, cl.Service) {
					cl.Service = ""
					cl.ConfigField = ""
					*diagnostics = append(*diagnostics, servicecatalog.Diagnostic{Code: "client_metadata_unresolved", Component: provider, Source: source})
				} else {
					cl.Resolution = "declared"
				}
				// The provider, the func making the call, and the call's
				// ordinal in it: stable while that func's registrations are,
				// whatever moves around them.
				cl.ID = fmt.Sprintf("%s:%s#%d", provider, fd.Name.Name, calls)
				out = append(out, cl)
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// catalogFuncSymbol is fd's servicecatalog.Source symbol: "Name", or
// "Recv.Name" for a method ("" for a receiver it cannot name).
func catalogFuncSymbol(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	e := fd.Recv.List[0].Type
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name + "." + fd.Name.Name
		default:
			return ""
		}
	}
}

// catalogConfigFields is the set of non-sensitive string config fields a
// client may name. Validate against the existing descriptor's config fields;
// never create another endpoint registry or export resolved values/defaults.
func catalogConfigFields(dir string) map[string]bool {
	fields := map[string]bool{}
	desc, err := LoadDescriptor(dir)
	if err != nil || desc == nil {
		return fields
	}
	for _, cfg := range desc.Configs {
		for _, field := range cfg.Fields {
			if cfg.FullName != "" && field.ProtoType == "string" && !field.Sensitive {
				fields[cfg.FullName+"."+field.Name] = true
			}
		}
	}
	return fields
}

// isCatalogNewClient reports whether call invokes servicecatalog.NewClient,
// generic instantiation included, by its resolved object rather than its
// spelling.
func isCatalogNewClient(info *types.Info, call *ast.CallExpr) bool {
	fun := call.Fun
	if idx, ok := fun.(*ast.IndexExpr); ok {
		fun = idx.X
	}
	if idx, ok := fun.(*ast.IndexListExpr); ok {
		fun = idx.X
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == "github.com/reliant-labs/forge/pkg/servicecatalog" && fn.Name() == "NewClient"
}

// catalogClient reads a NewClient call's declared metadata: the constant
// Service and ConfigField of its first (composite literal) argument, and the
// constructor its second argument names.
func catalogClient(info *types.Info, call *ast.CallExpr) servicecatalog.Client {
	var cl servicecatalog.Client
	if len(call.Args) < 2 {
		return cl
	}
	if lit, ok := call.Args[0].(*ast.CompositeLit); ok {
		for _, el := range lit.Elts {
			key, v := catalogConstStringField(info, el)
			switch key {
			case "Service":
				cl.Service = v
			case "ConfigField":
				cl.ConfigField = v
			}
		}
	}
	if cs, ok := call.Args[1].(*ast.SelectorExpr); ok {
		if obj := info.Uses[cs.Sel]; obj != nil && obj.Pkg() != nil {
			cl.Constructor = obj.Pkg().Path() + "." + obj.Name()
		}
	}
	return cl
}

// catalogConstStringField returns the key and constant string value of a
// `Key: <const string>` literal element, or "" when el is anything else.
func catalogConstStringField(info *types.Info, el ast.Expr) (key, value string) {
	kv, ok := el.(*ast.KeyValueExpr)
	if !ok {
		return "", ""
	}
	ident, ok := kv.Key.(*ast.Ident)
	if !ok {
		return "", ""
	}
	v := info.Types[kv.Value].Value
	if v == nil || v.Kind() != constant.String {
		return "", ""
	}
	return ident.Name, constant.StringVal(v)
}

// Connect exports <Service>Name next to the constructor; compare that typed
// constant, not the constructor's spelling or package basename.
func catalogClientService(info *types.Info, call *ast.CallExpr, service string) bool {
	if len(call.Args) < 2 {
		return false
	}
	sel, ok := call.Args[1].(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	short := service[strings.LastIndex(service, ".")+1:]
	obj, ok := fn.Pkg().Scope().Lookup(short + "Name").(*types.Const)
	return ok && obj.Val().Kind() == constant.String && constant.StringVal(obj.Val()) == service && fn.Name() == "New"+short+"Client"
}

func catalogProtoRef(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i, r := range part {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}
