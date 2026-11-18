package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/templates"
)

// The scaffolded handlers_crud.go is the last thing a handler author
// reads before writing a handler, so what it says about authentication
// is load-bearing. It used to say that "the CRUD lifecycle (auth,
// pagination, error mapping) lives in pkg/crud". pkg/crud has never
// authenticated anything. One measured run shipped 17 of 20 CRUD RPCs
// with no auth check at all — including a Create that accepted an
// anonymous POST and wrote a row — while all 20 declared
// auth_required: true in the proto.
//
// The declaration is enforced now: forge projects auth_required into
// pkg/middleware/procedures_gen.go and the interceptor runs fail-closed
// against it, so an unauthenticated caller no longer reaches a handler
// that did not publish itself. What pkg/crud still cannot do is tell one
// authenticated caller from another, which is why the seam below is
// still named on every method — the question it answers moved from "is
// anyone there?" to "which rows are theirs?".
//
// These assertions pin the file's honesty without quoting a sentence
// from it or naming a file it does not own: the delegate package is read
// from the import block the emitter stamps into every shim, its
// capabilities are resolved against its real exported surface, and the
// auth seam is resolved against the middleware forge actually scaffolds.
// Every derived set is checked for emptiness first, because an empty set
// would make every assertion below pass vacuously.

// authVocabulary is the identifier stems that mean "this code knows who
// the caller is". It is the one hardcoded thing here; it anchors the
// question, and both sides of the comparison are derived.
var authVocabulary = []string{"auth", "claim", "principal", "identity", "caller"}

func looksAuthShaped(ident string) bool {
	low := strings.ToLower(ident)
	for _, stem := range authVocabulary {
		if strings.Contains(low, stem) {
			return true
		}
	}
	return false
}

// delegatePackage returns the import path and on-disk directory of the
// package the scaffolded shim delegates to, read from the import list
// the emitter stamps into the file. Nothing here names pkg/crud: if the
// delegation ever moves, these assertions follow it.
func delegatePackage(t *testing.T) (importPath, dir string) {
	t.Helper()

	root := moduleRootDir(t)
	modPath := modulePathOf(t, root)

	stamped := crudShimImports(CRUDTemplateData{
		Module:       "example.com/app",
		ProtoPackage: "proto/services/patients",
		// One ordinary (non-shape-mismatch) method: the delegating
		// shape whose header makes the claims under test.
		CRUDMethods: []CRUDMethodTemplateData{{MethodName: "GetPatient"}},
	})

	var own []string
	for _, imp := range stamped {
		if strings.HasPrefix(imp, modPath+"/") {
			own = append(own, imp)
		}
	}
	if len(own) != 1 {
		t.Fatalf("expected the delegating shim to import exactly one %s package, got %v (from %v) — "+
			"this test can no longer tell which package the delegation claims to cover", modPath, own, stamped)
	}
	importPath = own[0]
	dir = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(importPath, modPath+"/")))
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("delegate %s does not resolve to a directory at %s: %v", importPath, dir, err)
	}
	return importPath, dir
}

// moduleRootDir walks up from the test's working directory to the go.mod
// that owns it.
func moduleRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

func modulePathOf(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no module directive in %s/go.mod", root)
	return ""
}

// exportedSurface is every exported identifier a package declares:
// package-level funcs and methods, types, consts, vars, and the fields
// of exported structs. Test files are excluded — a capability proven
// only by a test does not ship.
func exportedSurface(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	surface := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.IsExported() {
					surface[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						surface[s.Name.Name] = true
						st, ok := s.Type.(*ast.StructType)
						if !ok || st.Fields == nil {
							continue
						}
						for _, fld := range st.Fields.List {
							for _, n := range fld.Names {
								if n.IsExported() {
									surface[n.Name] = true
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								surface[n.Name] = true
							}
						}
					}
				}
			}
		}
	}
	if len(surface) == 0 {
		t.Fatalf("derived an EMPTY exported surface for %s — every capability assertion would pass vacuously", dir)
	}
	return surface
}

// TestCRUDShim_AdvertisesOnlyCapabilitiesTheDelegateHas is the structural
// half: the scaffolded header renders crudDelegatedCapabilities rather
// than prose, and every entry must name a symbol that exists.
func TestCRUDShim_AdvertisesOnlyCapabilitiesTheDelegateHas(t *testing.T) {
	importPath, dir := delegatePackage(t)
	surface := exportedSurface(t, dir)

	if len(crudDelegatedCapabilities) == 0 {
		t.Fatal("crudDelegatedCapabilities is empty — the scaffolded header would advertise nothing and this assertion would prove nothing")
	}
	for _, c := range crudDelegatedCapabilities {
		if !surface[c.Symbol] {
			t.Errorf("the scaffolded shim header tells every handler author that %s handles %q, "+
				"but %s exports no %s — nothing implements the claim",
				importPath, c.Label, importPath, c.Symbol)
		}
	}
}

// TestCRUDShim_ClaimsNoAuthCapability is the tripwire: the delegate
// exports nothing that could authenticate, so no capability may be
// advertised in those terms. It also fails in the other direction — if
// the delegate grows auth, the scaffold's ownership story is stale and
// has to be rewritten deliberately.
func TestCRUDShim_ClaimsNoAuthCapability(t *testing.T) {
	importPath, dir := delegatePackage(t)
	surface := exportedSurface(t, dir)

	var authy []string
	for sym := range surface {
		if looksAuthShaped(sym) {
			authy = append(authy, sym)
		}
	}
	if len(authy) > 0 {
		t.Fatalf("%s now exports %v, so it may authenticate. The scaffolded shim tells every handler "+
			"author that it does NOT and that authentication is theirs — re-derive that text before relaxing this test",
			importPath, authy)
	}
	for _, c := range crudDelegatedCapabilities {
		if looksAuthShaped(c.Label) {
			t.Errorf("capability %q is advertised as handled by %s, which exports nothing that knows who the caller is",
				c.Label, importPath)
		}
	}
}

// TestCRUDShim_TellsEveryHandlerAuthorAuthIsTheirs is the half that the
// measured defect needed. An author writing DeleteOrder reads
// DeleteOrder's comment, not the file header twenty lines up — in the
// measured run CreateOrder/GetOrder/UpdateOrder got a GetUser call and
// DeleteOrder/ListOrders, same entity and same file, did not. So the
// seam is named on every method, and the seam has to be real. It is named
// on the public methods too, in the negative: a public RPC says why
// calling it there would contradict the proto.
func TestCRUDShim_TellsEveryHandlerAuthorAuthIsTheirs(t *testing.T) {
	requireScaffoldedAuthSeam(t)

	header, methodDocs := scaffoldShimDocs(t)

	if len(methodDocs) == 0 {
		t.Fatal("the scaffold produced no handler methods — every per-method assertion would pass vacuously")
	}
	if !strings.Contains(header, crudAuthSeam) {
		t.Errorf("the scaffolded handlers_crud.go header never names %s, so nothing tells the author "+
			"that authenticating the caller is their job:\n%s", crudAuthSeam, header)
	}
	for _, name := range sortedKeys(methodDocs) {
		if !strings.Contains(methodDocs[name], crudAuthSeam) {
			t.Errorf("%s's doc comment never names %s — an author editing this method is told the "+
				"delegation handles the lifecycle and nothing tells it the caller is unauthenticated:\n%s",
				name, crudAuthSeam, methodDocs[name])
		}
	}
}

// TestCRUDShim_HeaderRendersTheCapabilityList keeps the capability list
// load-bearing. If the header goes back to prose, the list stops being
// what the file says and the assertions above stop guarding anything.
func TestCRUDShim_HeaderRendersTheCapabilityList(t *testing.T) {
	header, _ := scaffoldShimDocs(t)
	for _, c := range crudDelegatedCapabilities {
		if !strings.Contains(header, c.Label) {
			t.Errorf("crudDelegatedCapabilities advertises %q but the scaffolded header does not render it — "+
				"the header is describing the delegation in prose again, and the list guards nothing:\n%s",
				c.Label, header)
		}
	}
}

// requireScaffoldedAuthSeam proves the seam the scaffold points at is a
// function forge really scaffolds, by finding its declaration in the
// project template tree. Names no file: it searches every project
// template for the package.
func requireScaffoldedAuthSeam(t *testing.T) {
	t.Helper()
	names, err := templates.ProjectTemplates().List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("the project template tree is empty — the seam assertion would pass vacuously")
	}
	var sawPackage bool
	fset := token.NewFileSet()
	for _, name := range names {
		// pkg/middleware/middleware.go is RENDERED (middleware.go.tmpl: it
		// names the project's session cookie), so .go.tmpl is in scope. A
		// template whose actions are not valid Go fails to parse and is
		// skipped below, exactly as a non-Go file would be.
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".go.tmpl") {
			continue
		}
		data, err := templates.ProjectTemplates().Get(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil || f.Name == nil || f.Name.Name != crudAuthSeamPkg {
			continue
		}
		sawPackage = true
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == crudAuthSeamFunc {
				return
			}
		}
	}
	if !sawPackage {
		t.Fatalf("no project template declares package %q — the scaffolded shim points handler authors at a package forge does not ship", crudAuthSeamPkg)
	}
	t.Fatalf("package %q is scaffolded but declares no %s — the scaffolded shim points every handler author at a function that does not exist",
		crudAuthSeamPkg, crudAuthSeamFunc)
}

// scaffoldShimDocs runs the real generator over a minimal service and
// returns the birth file's package doc and every handler method's doc
// comment, parsed rather than string-matched.
func scaffoldShimDocs(t *testing.T) (header string, methodDocs map[string]string) {
	t.Helper()
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceGo := `package patients

import "github.com/reliant-labs/forge/pkg/orm"

type Deps struct {
	DB orm.Context
}

type Service struct {
	deps Deps
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	entities := []EntityDef{{
		Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
	}}
	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			// AuthRequired mirrors the descriptor default (unannotated
			// RPCs are auth_required: true) for Create, and the explicit
			// opt-out for Delete, so both comment branches are exercised.
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse", AuthRequired: true},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse", AuthRequired: true},
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse", AuthRequired: true},
			{Name: "UpdatePatient", InputType: "UpdatePatientRequest", OutputType: "UpdatePatientResponse", AuthRequired: true},
			{Name: "DeletePatient", InputType: "DeletePatientRequest", OutputType: "DeletePatientResponse", AuthRequired: false},
		},
	}

	checksums.ResetSkipWrite()
	cs := &checksums.FileChecksums{}
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	src, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("handlers_crud.go not scaffolded: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "handlers_crud.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("scaffolded handlers_crud.go is not valid Go: %v\n%s", err, src)
	}

	methodDocs = map[string]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			continue
		}
		methodDocs[fn.Name.Name] = fn.Doc.Text()
	}
	return f.Doc.Text(), methodDocs
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
