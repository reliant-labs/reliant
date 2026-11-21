package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
)

// reconcileMountHelper keeps a connect-go v2 service.go's mount helper named
// what mountMethodFor says, so the generated mounts, route sets, test helpers
// and mock — which all call the resolved name — find it.
//
// service.go is scaffolded once, usually before the service declares any
// RPC, so it is born with RegisterV2(server *connect.Server). An RPC called
// RegisterV2 added to the proto later collides with it: the stub scan
// (ScanExistingMethods) would count the helper as that RPC's implementation
// and emit no stub, *Service would not satisfy the generated handler
// interface, and nothing calling MountV2 would compile. Likewise for a MountV2
// RPC once the helper is MountV2, and so on down the candidates
// (mountMethodCandidateV2). So when the helper's name is not the resolved one,
// its declaration — and its doc comment's leading word — is renamed in place,
// and nothing else in the owned file is touched (the same surgical edit
// reconcileConnectIdentity makes). As RPCs are removed it moves back toward
// the earliest free candidate.
//
// A disowned helper is never rewritten. Its callers keep its actual name.
// Connect-go v1's Register/Mount behavior is not reconciled.
func reconcileMountHelper(svc ServiceDef, root, dir string, cs *checksums.FileChecksums) error {
	if !connectV2For(svc, root) {
		return nil
	}
	mount, err := resolveMountHelper(svc, true, root, dir, cs)
	if err != nil {
		return err
	}
	if mount.helper == nil || mount.helper.Name.Name == mount.name {
		return nil
	}
	helper, src, fset, want := mount.helper, mount.src, mount.fset, mount.name
	from := helper.Name.Name
	path := filepath.Join(dir, "service.go")

	// The helper's name, and the doc comment's leading word when it names
	// the helper; spans in file order, applied back to front so the earlier
	// offset stays valid.
	var spans [][2]int
	if helper.Doc != nil && len(helper.Doc.List) > 0 {
		if c := helper.Doc.List[0]; strings.HasPrefix(c.Text, "// "+from+" ") {
			start := fset.Position(c.Pos()).Offset + len("// ")
			spans = append(spans, [2]int{start, start + len(from)})
		}
	}
	spans = append(spans, [2]int{fset.Position(helper.Name.Pos()).Offset, fset.Position(helper.Name.End()).Offset})
	out := src
	for i := len(spans) - 1; i >= 0; i-- {
		out = append(append(append([]byte{}, out[:spans[i][0]]...), want...), out[spans[i][1]:]...)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, out, info.Mode().Perm())
}

type mountHelperResolution struct {
	name   string
	helper *ast.FuncDecl // non-nil only when Forge may rename it
	src    []byte
	fset   *token.FileSet
}

// resolveMountHelper is read by both caller generation and the reconciler.
// Only the actual registrar on service.go is free to move; every other
// method (including value receivers and retired RPCs) reserves its name.
func resolveMountHelper(svc ServiceDef, v2 bool, root, dir string, cs *checksums.FileChecksums) (mountHelperResolution, error) {
	result := mountHelperResolution{name: mountMethodFor(svc, v2)}
	if !v2 || dir == "" {
		return result, nil
	}
	path := filepath.Join(dir, "service.go")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return result, nil // not scaffolded yet
	}
	if err != nil {
		return result, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return result, nil // leave a half-edited owned file to the compiler
	}
	helper := findV2MountHelper(file)
	if helper == nil {
		return result, nil
	}
	if root != "" {
		if cs == nil {
			cs, err = checksums.Load(root)
			if err != nil {
				return result, err
			}
		}
		if rel, err := filepath.Rel(root, path); err == nil && cs.IsDisowned(filepath.ToSlash(rel)) {
			result.name = helper.Name.Name
			for _, method := range svc.Methods {
				if method.GoName() == result.name {
					return result, fmt.Errorf("disowned %s: rename mount helper %s before adding the same-named RPC", path, result.name)
				}
			}
			return result, nil
		}
	}
	occupied, err := occupiedMountMethodNames(dir, file, helper)
	if err != nil {
		return result, err
	}
	return mountHelperResolution{name: availableMountMethodV2(svc, occupied), helper: helper, src: src, fset: fset}, nil
}

func findV2MountHelper(file *ast.File) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && isMountMethodCandidateV2(fn.Name.Name) && isV2MountHelper(fn) {
			return fn
		}
	}
	return nil
}

func occupiedMountMethodNames(dir string, service *ast.File, helper *ast.FuncDecl) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	occupied := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file := service
		if name != "service.go" {
			file, err = parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
			if err != nil || file.Name.Name != service.Name.Name {
				continue // malformed files fail at build; another package owns its methods
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn == helper || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Service" {
				occupied[fn.Name.Name] = true
			}
		}
	}
	return occupied, nil
}

// isV2MountHelper reports whether fn has the connect-go v2 mount helper's
// shape: func (s *Service) <name>(server *<pkg>.Server), no results.
func isV2MountHelper(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	if id, ok := star.X.(*ast.Ident); !ok || id.Name != "Service" {
		return false
	}
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		return false
	}
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	p, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := p.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Server"
}
