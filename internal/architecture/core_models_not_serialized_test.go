package architecture

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ─── step and scenario models never reach a serializer ─────────────────────
//
// core.StepExecution, core.ChatStepExecution and core.WorkflowScenario hold
// their nullable columns as pointers. Only encoding/json renders them in the
// pre-pointer sql.Null* shape (internal/db/core/legacy_json.go, pinned by
// legacy_json_test.go): a value that reaches encoding/json by ANY path renders
// the old bytes. The other encoders — gob, XML, YAML, protojson — are not
// covered by those methods, so one of the three models reaching one of them,
// directly, in a slice or map, as a field of another struct, or inside a
// composite literal handed to it, would change a persisted or public payload.
// This guard keeps them apart.
//
// How it stays fast and deterministic (an earlier version loaded ./... through
// go/packages, which type-checks the whole module in parallel: ~20 CPU-minutes
// under -race, a timeout, and a data race inside go/types):
//
//   - It works from `go/build` file lists, never from the go command, so it
//     depends on no build cache and spawns nothing.
//   - Only packages that import a serializer AND can reach internal/db/core are
//     inspected; any other package cannot mention a model.
//   - It type-checks SEQUENTIALLY with go/types, one fresh Checker, no goroutines.
//   - Inspected packages (and their tests) are checked with bodies; the module
//     packages that can reach core are checked declarations-only; every other
//     import is an empty stub package, because a package that cannot reach core
//     cannot carry a model. GOOS/GOARCH are pinned, so the result is the same
//     on every machine.
//
// A call whose argument types do not fully resolve (a stubbed package is in the
// expression) is still checked: its composite-literal elements, address-of and
// conversion operands, and call arguments are examined one by one.

var serializedModels = map[string]bool{"StepExecution": true, "ChatStepExecution": true, "WorkflowScenario": true}

const corePkgSuffix = "/internal/db/core"

// serializers maps an import path to the name a package of that path is
// declared with (the stub packages are named from this, so call sites resolve).
var serializers = map[string]string{
	"encoding/gob":             "gob",
	"encoding/xml":             "xml",
	"gopkg.in/yaml.v2":         "yaml",
	"gopkg.in/yaml.v3":         "yaml",
	"go.yaml.in/yaml/v2":       "yaml",
	"go.yaml.in/yaml/v3":       "yaml",
	"go.yaml.in/yaml/v4":       "yaml",
	"github.com/goccy/go-yaml": "yaml",
	"sigs.k8s.io/yaml":         "yaml",
	"google.golang.org/protobuf/encoding/protojson": "protojson",
}

func serializerName(p string) (string, bool) {
	if n, ok := serializers[p]; ok {
		return n, true
	}
	for base, n := range serializers {
		if strings.HasPrefix(p, base+"/") {
			return n, true
		}
	}
	return "", false
}

var serializerMethods = map[string]bool{
	"Encode": true, "EncodeElement": true, "Decode": true, "DecodeElement": true,
	"Marshal": true, "Unmarshal": true, "MarshalIndent": true,
}

type modulePkg struct {
	path string
	dir  string
	bp   *build.Package
}

func (mp *modulePkg) allImports() []string {
	var out []string
	for _, l := range [][]string{mp.bp.Imports, mp.bp.TestImports, mp.bp.XTestImports} {
		out = append(out, l...)
	}
	return out
}

func (mp *modulePkg) importsSerializer() bool {
	for _, imp := range mp.allImports() {
		if _, ok := serializerName(imp); ok {
			return true
		}
	}
	return false
}

type moduleIndex struct {
	modulePath string
	pkgs       map[string]*modulePkg
	reach      map[string]bool
}

func indexModule(root string, extraDirs ...string) (*moduleIndex, error) {
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	m := regexp.MustCompile(`(?m)^module\s+(\S+)`).FindSubmatch(gomod)
	if m == nil {
		return nil, errors.New("no module line in go.mod")
	}
	idx := &moduleIndex{modulePath: string(m[1]), pkgs: map[string]*modulePkg{}, reach: map[string]bool{}}
	ctxt := build.Default
	ctxt.CgoEnabled = false
	ctxt.GOOS, ctxt.GOARCH = "linux", "amd64"

	// Directory discovery is sequential; reading each directory's file list and
	// imports (go/build) is independent work and runs in parallel. Results land
	// in a slice by index, so the map is built in a fixed order.
	var dirs []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		name := d.Name()
		if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor" || name == "frontends") {
			return filepath.SkipDir
		}
		if p != root {
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		dirs = append(dirs, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, d := range extraDirs {
		dirs = append(dirs, filepath.Join(root, d))
	}
	type result struct {
		mp  *modulePkg
		err error
	}
	results := make([]result, len(dirs))
	var wg sync.WaitGroup
	work := make(chan int)
	for w := 0; w < runtime.GOMAXPROCS(0); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				dir := dirs[i]
				bp, err := ctxt.ImportDir(dir, 0)
				if err != nil {
					var noGo *build.NoGoError
					if !errors.As(err, &noGo) {
						results[i].err = fmt.Errorf("%s: %w", dir, err)
					}
					continue
				}
				rel, _ := filepath.Rel(root, dir)
				ip := idx.modulePath
				if rel != "." {
					ip = path.Join(idx.modulePath, filepath.ToSlash(rel))
				}
				results[i].mp = &modulePkg{path: ip, dir: dir, bp: bp}
			}
		}()
	}
	for i := range dirs {
		work <- i
	}
	close(work)
	wg.Wait()
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		if r.mp != nil {
			idx.pkgs[r.mp.path] = r.mp
		}
	}
	return idx, nil
}

func (idx *moduleIndex) isModule(p string) bool {
	return p == idx.modulePath || strings.HasPrefix(p, idx.modulePath+"/")
}

// reachesCore reports whether p's non-test imports lead to internal/db/core.
func (idx *moduleIndex) reachesCore(p string) bool {
	if r, ok := idx.reach[p]; ok {
		return r
	}
	idx.reach[p] = false // cycle guard; module packages are acyclic
	mp := idx.pkgs[p]
	r := false
	switch {
	case mp == nil:
	case strings.HasSuffix(p, corePkgSuffix):
		r = true
	default:
		for _, imp := range mp.bp.Imports {
			if idx.reachesCore(imp) {
				r = true
				break
			}
		}
	}
	idx.reach[p] = r
	return r
}

// checker type-checks module packages from source, sequentially. Only PARSING
// runs in parallel (prefetch): go/parser has no shared mutable state, and the
// checker reads the finished files in a fixed order, so the result does not
// depend on scheduling. (The race the old version hit was inside go/types.)
type checker struct {
	idx    *moduleIndex
	fset   *token.FileSet
	deps   map[string]*types.Package
	parsed map[string]*ast.File
}

func (c *checker) prefetch(paths []string) {
	files := make([]*ast.File, len(paths))
	var wg sync.WaitGroup
	work := make(chan int)
	for w := 0; w < runtime.GOMAXPROCS(0); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				files[i], _ = parser.ParseFile(c.fset, paths[i], nil, parser.SkipObjectResolution)
			}
		}()
	}
	for i := range paths {
		work <- i
	}
	close(work)
	wg.Wait()
	for i, p := range paths {
		if files[i] != nil {
			c.parsed[p] = files[i]
		}
	}
}

func (c *checker) parse(dir string, files []string) []*ast.File {
	var out []*ast.File
	for _, f := range files {
		if af := c.parsed[filepath.Join(dir, f)]; af != nil {
			out = append(out, af)
		}
	}
	return out
}

// neededFiles is every file the scan will read: each root's own and test
// files, and the non-test files of the core-reaching module packages they
// import.
func (idx *moduleIndex) neededFiles(roots []*modulePkg) []string {
	seen := map[string]bool{}
	var out []string
	add := func(mp *modulePkg, names []string) {
		for _, n := range names {
			p := filepath.Join(mp.dir, n)
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	visited := map[string]bool{}
	var dep func(string)
	dep = func(p string) {
		mp := idx.pkgs[p]
		if visited[p] || mp == nil || !idx.reachesCore(p) {
			return
		}
		visited[p] = true
		add(mp, mp.bp.GoFiles)
		for _, imp := range mp.bp.Imports {
			dep(imp)
		}
	}
	for _, mp := range roots {
		add(mp, mp.bp.GoFiles)
		add(mp, mp.bp.TestGoFiles)
		add(mp, mp.bp.XTestGoFiles)
		for _, imp := range mp.allImports() {
			dep(imp)
		}
	}
	sort.Strings(out)
	return out
}

func importsSerializerFile(f *ast.File) bool {
	for _, is := range f.Imports {
		if _, ok := serializerName(strings.Trim(is.Path.Value, `"`)); ok {
			return true
		}
	}
	return false
}

func stubName(p string) string {
	if n, ok := serializerName(p); ok {
		return n
	}
	elems := strings.Split(p, "/")
	n := elems[len(elems)-1]
	if regexp.MustCompile(`^v[0-9]+$`).MatchString(n) && len(elems) > 1 {
		n = elems[len(elems)-2]
	}
	if i := strings.Index(n, ".v"); i > 0 {
		n = n[:i]
	}
	return strings.NewReplacer("-", "_", ".", "_").Replace(n)
}

func (c *checker) Import(p string) (*types.Package, error) {
	if p == "unsafe" {
		return types.Unsafe, nil
	}
	if tp, ok := c.deps[p]; ok {
		return tp, nil
	}
	if !c.idx.isModule(p) || !c.idx.reachesCore(p) {
		stub := types.NewPackage(p, stubName(p))
		stub.MarkComplete()
		c.deps[p] = stub
		return stub, nil
	}
	mp := c.idx.pkgs[p]
	conf := types.Config{Importer: c, IgnoreFuncBodies: true, Error: func(error) {}, DisableUnusedImportCheck: true}
	tp, _ := conf.Check(p, c.fset, c.parse(mp.dir, mp.bp.GoFiles), nil)
	c.deps[p] = tp
	return tp, nil
}

// xtestImporter serves an external test package the test-augmented package
// under test, the way the go tool builds it.
type xtestImporter struct {
	c    *checker
	path string
	pkg  *types.Package
}

func (x xtestImporter) Import(p string) (*types.Package, error) {
	if p == x.path && x.pkg != nil {
		return x.pkg, nil
	}
	return x.c.Import(p)
}

func carriesSerializedModel(ty types.Type, seen map[types.Type]bool) bool {
	if ty == nil {
		return false
	}
	ty = types.Unalias(ty)
	if seen[ty] {
		return false
	}
	seen[ty] = true
	switch x := ty.(type) {
	case *types.Pointer:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Slice:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Array:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Map:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Named:
		if o := x.Obj(); o.Pkg() != nil && strings.HasSuffix(o.Pkg().Path(), corePkgSuffix) && serializedModels[o.Name()] {
			return true
		}
		return carriesSerializedModel(x.Underlying(), seen)
	case *types.Struct:
		for i := 0; i < x.NumFields(); i++ {
			f := x.Field(i)
			if !f.Exported() && !f.Embedded() || strings.Contains(x.Tag(i), `json:"-"`) {
				continue
			}
			if carriesSerializedModel(f.Type(), seen) {
				return true
			}
		}
	}
	return false
}

// carriedBy reports whether the VALUE of e can hold a model: its static type
// does, or it is built from an operand that does — a composite literal element
// (map[string]any{"rows": rows}), an address-of, a parenthesis, a conversion,
// or, when the callee did not resolve, any call argument.
func carriedBy(e ast.Expr, info *types.Info) bool {
	if tv, ok := info.Types[e]; ok && tv.Type != nil && carriesSerializedModel(tv.Type, map[types.Type]bool{}) {
		return true
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return carriedBy(x.X, info)
	case *ast.UnaryExpr:
		return carriedBy(x.X, info)
	case *ast.StarExpr:
		return carriedBy(x.X, info)
	case *ast.CompositeLit:
		for _, el := range x.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			if carriedBy(el, info) {
				return true
			}
		}
	case *ast.CallExpr:
		tv, ok := info.Types[x.Fun]
		resolved := ok && tv.Type != nil && tv.Type != types.Typ[types.Invalid]
		if tv.IsType() || !resolved { // a conversion, or a callee we cannot see
			for _, a := range x.Args {
				if carriedBy(a, info) {
					return true
				}
			}
		}
	}
	return false
}

type scanResult struct {
	checked    int
	violations []string
}

func (c *checker) scanRoot(mp *modulePkg, res *scanResult) {
	type unit struct {
		path  string
		files []string
	}
	units := []unit{{mp.path, append(append([]string{}, mp.bp.GoFiles...), mp.bp.TestGoFiles...)}}
	if len(mp.bp.XTestGoFiles) > 0 {
		units = append(units, unit{mp.path + "_test", mp.bp.XTestGoFiles})
	}
	var internal *types.Package
	for _, u := range units {
		files := c.parse(mp.dir, u.files)
		for _, f := range files {
			if !importsSerializerFile(f) {
				// Only a file that imports a serializer can call one; the
				// others contribute declarations, not bodies, to the check.
				for _, d := range f.Decls {
					if fd, ok := d.(*ast.FuncDecl); ok {
						fd.Body = nil
					}
				}
			}
		}
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
		var imp types.Importer = c
		if strings.HasSuffix(u.path, "_test") {
			imp = xtestImporter{c, mp.path, internal}
		}
		conf := types.Config{Importer: imp, Error: func(error) {}, DisableUnusedImportCheck: true}
		tp, _ := conf.Check(u.path, c.fset, files, info)
		if !strings.HasSuffix(u.path, "_test") {
			internal = tp
		}
		for _, f := range files {
			if !importsSerializerFile(f) {
				continue
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				serializer := false
				if id, ok := sel.X.(*ast.Ident); ok {
					if pn, ok := info.Uses[id].(*types.PkgName); ok {
						_, serializer = serializerName(pn.Imported().Path())
					}
				}
				if !serializer && serializerMethods[sel.Sel.Name] {
					// An encoder or decoder value: its type came from a stubbed
					// package, so it is invalid or unknown; a method call on a
					// resolved module type is not a serializer.
					rt := info.TypeOf(sel.X)
					serializer = rt == nil || rt == types.Typ[types.Invalid]
				}
				if !serializer {
					return true
				}
				res.checked++
				for _, arg := range call.Args {
					if carriedBy(arg, info) {
						pos := c.fset.Position(call.Pos())
						res.violations = append(res.violations, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(pos.Filename), pos.Line, types.ExprString(call.Fun)))
					}
				}
				return true
			})
		}
	}
}

// scanModule inspects every package accepted by only that imports a serializer
// and can reach internal/db/core.
func scanModule(idx *moduleIndex, only func(*modulePkg) bool) *scanResult {
	c := &checker{idx: idx, fset: token.NewFileSet(), deps: map[string]*types.Package{}, parsed: map[string]*ast.File{}}
	var roots []*modulePkg
	for _, mp := range idx.pkgs {
		if only(mp) && mp.importsSerializer() && idx.reachesCore(mp.path) {
			roots = append(roots, mp)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].path < roots[j].path })
	c.prefetch(idx.neededFiles(roots))
	res := &scanResult{}
	for _, mp := range roots {
		c.scanRoot(mp, res)
	}
	sort.Strings(res.violations)
	return res
}

// verdict is the guard's judgement of a scan: zero serializer calls is a
// failure (the scan is not seeing the code), and so is any violation.
func verdict(res *scanResult) error {
	if res.checked == 0 {
		return errors.New("found no serializer calls at all: the scan is not seeing the code it guards")
	}
	if len(res.violations) > 0 {
		return fmt.Errorf("core step/scenario models reach a serializer that does not keep their pre-pointer sql.Null* shape (only encoding/json does, via internal/db/core/legacy_json.go):\n%s", strings.Join(res.violations, "\n"))
	}
	return nil
}

var (
	indexOnce sync.Once
	theIndex  *moduleIndex
	indexErr  error
)

func guardIndex(t *testing.T) *moduleIndex {
	t.Helper()
	indexOnce.Do(func() {
		theIndex, indexErr = indexModule("../..",
			"internal/architecture/testdata/violating",
			"internal/architecture/testdata/clean",
			"internal/architecture/testdata/nocalls")
	})
	if indexErr != nil {
		t.Fatal(indexErr)
	}
	return theIndex
}

func isFixture(mp *modulePkg) bool {
	return strings.Contains(mp.path, "/internal/architecture/testdata/")
}

func TestStepAndScenarioModelsAreNeverSerialized(t *testing.T) {
	idx := guardIndex(t)
	res := scanModule(idx, func(mp *modulePkg) bool { return !isFixture(mp) })
	if err := verdict(res); err != nil {
		t.Fatal(err)
	}
	t.Logf("checked %d serializer calls", res.checked)
}

// The negative proofs: the guard must be able to fail, with file:line, and
// must fail when it sees nothing.
func TestSerializerGuardFailsOnAViolation(t *testing.T) {
	idx := guardIndex(t)
	res := scanModule(idx, func(mp *modulePkg) bool { return strings.HasSuffix(mp.path, "/testdata/violating") })
	err := verdict(res)
	if err == nil {
		t.Fatal("a StepExecution handed to xml.Marshal inside a struct was not reported")
	}
	if !strings.Contains(err.Error(), "testdata/violating/violating.go:") {
		t.Fatalf("the violation does not name file:line: %v", err)
	}
	if len(res.violations) != 3 {
		t.Fatalf("want 3 violations (field of a struct, a map literal element, an unresolved wrapper call), got %d:\n%s", len(res.violations), strings.Join(res.violations, "\n"))
	}
}

func TestSerializerGuardFailsWhenItSeesNoSerializerCalls(t *testing.T) {
	idx := guardIndex(t)
	res := scanModule(idx, func(mp *modulePkg) bool { return strings.HasSuffix(mp.path, "/testdata/nocalls") })
	if err := verdict(res); err == nil || !strings.Contains(err.Error(), "no serializer calls") {
		t.Fatalf("a scan that checked nothing must fail, got %v", err)
	}
}

func TestSerializerGuardAcceptsAValueThatCarriesNoModel(t *testing.T) {
	idx := guardIndex(t)
	res := scanModule(idx, func(mp *modulePkg) bool { return strings.HasSuffix(mp.path, "/testdata/clean") })
	if err := verdict(res); err != nil {
		t.Fatalf("a serializer call over a value with no model was rejected: %v", err)
	}
	if res.checked == 0 {
		t.Fatal("the clean fixture's serializer call was not counted")
	}
}
