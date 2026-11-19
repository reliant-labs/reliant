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

	"github.com/reliant-labs/forge/internal/checksums"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// reconcileConnectIdentity updates only equivalent service-name selectors on
// the service's actual Connect import. Never re-render the owned service file:
// Deps, constructor, HTTP routes and formatting all belong to the author.
func reconcileConnectIdentity(svc ServiceDef, root, dir string, cs *checksums.FileChecksums) error {
	if svc.GoPackage == "" || svc.PkgName == "" {
		return nil
	}
	if cs == nil {
		var err error
		cs, err = checksums.Load(root)
		if err != nil {
			return err
		}
	}
	path := filepath.Join(dir, "service.go")
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if cs.IsDisowned(filepath.ToSlash(rel)) {
		return nil
	}
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, path, src, 0)
	if err != nil {
		return err
	}
	aliases := map[string]bool{}
	connectPkg := svc.PkgName + "connect"
	for _, imp := range file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != svc.GoPackage+"/"+connectPkg {
			continue
		}
		alias := connectPkg
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		aliases[alias] = true
	}
	type replacement struct {
		start, end int
		text       string
	}
	var edits []replacement
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !aliases[pkg.Name] {
			return true
		}
		for _, shape := range []struct{ prefix, suffix, target string }{
			{"Unimplemented", "Handler", svc.GoName()},
			{"New", "Handler", svc.GoName()},
			{"", "Name", svc.Name},
		} {
			old := sel.Sel.Name
			if !strings.HasPrefix(old, shape.prefix) || !strings.HasSuffix(old, shape.suffix) {
				continue
			}
			base := strings.TrimSuffix(strings.TrimPrefix(old, shape.prefix), shape.suffix)
			target := shape.prefix + shape.target + shape.suffix
			if old != target && strings.EqualFold(base, shape.target) {
				edits = append(edits, replacement{fs.Position(sel.Sel.Pos()).Offset, fs.Position(sel.Sel.End()).Offset, target})
			}
		}
		return true
	})
	if len(edits) == 0 {
		return nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, edit := range edits {
		src = append(append(append([]byte{}, src[:edit.start]...), []byte(edit.text)...), src[edit.end:]...)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, src, info.Mode().Perm())
}
