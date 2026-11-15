package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// operatorkit.go — `forge fix operatorkit`, the v0.1.46 operator-bridge move
// (#918): lifecyclekit re-exported operatorkit's Run, Controller and Options,
// so every binary importing lifecyclekit for Supervise or WrapWorker linked
// controller-runtime. The three names now live only in operatorkit. The
// signatures did not change, so the rewrite is a rename and nothing else —
// which is why `forge project upgrade` runs it on its own.

const (
	lifecyclekitPath = "github.com/reliant-labs/forge/pkg/lifecyclekit"
	operatorkitPath  = "github.com/reliant-labs/forge/pkg/appkit/operatorkit"
)

// movedToOperatorkit are the lifecyclekit names #918 moved.
var movedToOperatorkit = map[string]bool{"Run": true, "Controller": true, "Options": true}

func init() {
	register(Codemod{
		Name:    "operatorkit",
		Version: "v0.1.46",
		Short:   "Rename lifecyclekit.Run / Controller / Options to operatorkit (moved in v0.1.46)",
		Long: `Rename the operator bridge lifecyclekit no longer re-exports.

forge v0.1.46 moved Run, Controller and Options out of
github.com/reliant-labs/forge/pkg/lifecyclekit and into
github.com/reliant-labs/forge/pkg/appkit/operatorkit, so that a binary
importing lifecyclekit for Supervise, WrapWorker or Singleton never links
controller-runtime. The signatures are identical.

This rewrites every reference in the project's own Go files (generated files,
vendor/ and testdata/ are left alone), adds the operatorkit import, and drops
the lifecyclekit import from a file that no longer uses it. It is a rename
and nothing else, so ` + "`forge project upgrade`" + ` runs it on its own when the
project's baseline predates v0.1.46. Re-running it changes nothing.`,
		Upgrade: true,
		Scoped:  true,
		Run:     runOperatorkit,
	})
}

func runOperatorkit(dir string, opts Options) (Report, error) {
	var rep Report
	err := ownedGoFiles(dir, func(rel string, src []byte) error {
		if !opts.Scope.Contains(rel) || !bytes.Contains(src, []byte(lifecyclekitPath)) {
			return nil
		}
		out, moved, err := rewriteOperatorkit(rel, src)
		if err != nil {
			return err
		}
		if len(moved) == 0 {
			return nil
		}
		rep.Rewrites = append(rep.Rewrites, fmt.Sprintf("%s: lifecyclekit.%s → operatorkit", rel, strings.Join(moved, ", lifecyclekit.")))
		if !opts.Write {
			return nil
		}
		return writeFile(filepath.Join(dir, filepath.FromSlash(rel)), out)
	})
	if err != nil {
		return Report{}, err
	}
	if rep.Changed() {
		rep.Verify = []string{"go build ./..."}
	}
	return rep, nil
}

// rewriteOperatorkit renames one file's references. It returns the new
// source and the distinct names it moved (sorted); no names means the file
// is unchanged.
func rewriteOperatorkit(rel string, src []byte) ([]byte, []string, error) {
	fset := token.NewFileSet()
	// Object resolution stays ON: an identifier that names the package has
	// no Obj, while a local variable that happens to share the import's name
	// does — which is how a shadowing local is told apart from the package.
	file, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	lcName := importName(file, lifecyclekitPath, "lifecyclekit")
	if lcName == "" {
		return src, nil, nil
	}
	refs := packageRefs(file, lcName, movedToOperatorkit)
	if len(refs) == 0 {
		return src, nil, nil
	}

	opName := importName(file, operatorkitPath, "operatorkit")
	addImport := opName == ""
	if addImport {
		opName = "operatorkit"
		if nameTaken(file, opName) {
			opName = "forgeoperatorkit"
		}
	}

	// Splice the package identifier of each reference, back to front, so
	// every other byte of the file — comments, alignment — stays as it was.
	movedSet := map[string]bool{}
	sort.Slice(refs, func(i, j int) bool { return refs[i].X.Pos() > refs[j].X.Pos() })
	out := append([]byte(nil), src...)
	for _, sel := range refs {
		start := fset.Position(sel.X.Pos()).Offset
		end := fset.Position(sel.X.End()).Offset
		out = append(out[:start], append([]byte(opName), out[end:]...)...)
		movedSet[sel.Sel.Name] = true
	}

	// Then the imports, on the spliced source.
	fset = token.NewFileSet()
	file, err = parser.ParseFile(fset, rel, out, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("re-parse %s after the rename: %w", rel, err)
	}
	if addImport {
		name := ""
		if opName != "operatorkit" {
			name = opName
		}
		astutil.AddNamedImport(fset, file, name, operatorkitPath)
	}
	if len(packageRefs(file, lcName, nil)) == 0 {
		name := ""
		if lcName != "lifecyclekit" {
			name = lcName
		}
		astutil.DeleteNamedImport(fset, file, name, lifecyclekitPath)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, nil, fmt.Errorf("format %s: %w", rel, err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, nil, fmt.Errorf("format %s: %w", rel, err)
	}
	moved := make([]string, 0, len(movedSet))
	for n := range movedSet {
		moved = append(moved, n)
	}
	sort.Strings(moved)
	return formatted, moved, nil
}

// importName is the name file refers to the package at path by: its
// explicit import name, or def for an unnamed import. "" when the file does
// not import it, or imports it blank or dot (no selector names it).
func importName(file *ast.File, path, def string) string {
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != path {
			continue
		}
		if spec.Name == nil {
			return def
		}
		if spec.Name.Name == "_" || spec.Name.Name == "." {
			return ""
		}
		return spec.Name.Name
	}
	return ""
}

// packageRefs returns the selector expressions pkg.<Sel> in file whose
// left side is the imported package (not a local of the same name), keeping
// only the selectors in only when it is non-nil.
func packageRefs(file *ast.File, pkg string, only map[string]bool) []*ast.SelectorExpr {
	var refs []*ast.SelectorExpr
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != pkg || x.Obj != nil {
			return true
		}
		if only == nil || only[sel.Sel.Name] {
			refs = append(refs, sel)
		}
		return true
	})
	return refs
}

// nameTaken reports whether name is already an import name or a package-level
// declaration in file, so a new import under it would collide.
func nameTaken(file *ast.File, name string) bool {
	for _, spec := range file.Imports {
		if spec.Name != nil && spec.Name.Name == name {
			return true
		}
		if spec.Name == nil && filepath.Base(strings.Trim(spec.Path.Value, `"`)) == name {
			return true
		}
	}
	return file.Scope != nil && file.Scope.Lookup(name) != nil
}
