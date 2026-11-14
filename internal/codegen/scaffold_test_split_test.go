package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scaffoldSplitService is a two-owner service: the order lifecycle and the
// prescription lifecycle, in one handler package, exactly as peptides-rw1
// had them.
func scaffoldSplitService() ServiceDef {
	return ServiceDef{
		Name:       "AdminService",
		Package:    "admin.v1",
		GoPackage:  "example.com/app/gen/services/admin/v1",
		PkgName:    "adminv1",
		ProtoFile:  "proto/services/admin/v1/admin.proto",
		ModulePath: "example.com/app",
		Methods: []Method{
			{Name: "SubmitOrder", InputType: "SubmitOrderRequest", OutputType: "SubmitOrderResponse"},
			{Name: "ShipOrder", InputType: "ShipOrderRequest", OutputType: "ShipOrderResponse"},
			{Name: "SubmitPrescription", InputType: "SubmitPrescriptionRequest", OutputType: "SubmitPrescriptionResponse"},
		},
	}
}

// TestScaffoldTests_SurviveTwoOwners is the reason the split exists.
//
// peptides-rw1 birthed every RPC's rows and one package-level `setup()`
// into a single handlers_scaffold_test.go. Two agents owned different RPCs
// in that one package, could not both own the file, and the root DELETED it
// before either wrote a test — forge's self-destructing red-goes-green
// mechanism thrown away for structural reasons. The rule iteration 0 wrote
// down: anything scaffold-once must survive the way forge's own output says
// to grow.
//
// So: one file per RPC, and NOTHING at package scope.
func TestScaffoldTests_SurviveTwoOwners(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "admin")
	if err := GenerateServiceStub(scaffoldSplitService(), targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}

	// One file per RPC, named after the RPC alone — so which file an owner
	// touches is decided by which RPC they implement, with nothing to agree
	// on beforehand.
	want := map[string]string{
		"handlers_scaffold_submit_order_test.go":        "TestSubmitOrder_Generated",
		"handlers_scaffold_ship_order_test.go":          "TestShipOrder_Generated",
		"handlers_scaffold_submit_prescription_test.go": "TestSubmitPrescription_Generated",
	}
	for name, fn := range want {
		body, err := os.ReadFile(filepath.Join(targetDir, name))
		if err != nil {
			t.Fatalf("expected a scaffold test per RPC; %s missing: %v", name, err)
		}
		if !strings.Contains(string(body), fn) {
			t.Errorf("%s should declare %s:\n%s", name, fn, body)
		}
	}
	if _, err := os.Stat(filepath.Join(targetDir, "handlers_scaffold_test.go")); err == nil {
		t.Error("the single shared scaffold test must no longer be born — it is the file two owners collided on")
	}

	// The property that actually makes the split work: the files declare no
	// identifier in common, so they compile side by side and neither owner's
	// edit can break the other's. A package-level `setup()` in each would be
	// a redeclaration; a `setup()` in ONE of them would make deleting that
	// RPC's file break every other file.
	decls := map[string]string{} // identifier -> file that declared it
	for name := range want {
		path := filepath.Join(targetDir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, ident := range topLevelDeclNames(f) {
			if prev, dup := decls[ident]; dup {
				t.Errorf("%s and %s both declare %q at package scope — that is the collision", prev, name, ident)
			}
			decls[ident] = name
		}
	}

	// And nothing shared is even OFFERED: every declaration is the RPC's own
	// test function. `setup` bought one line of typing and cost the file its
	// ability to have more than one owner.
	for ident := range decls {
		if !strings.HasSuffix(ident, "_Generated") {
			t.Errorf("scaffold declares %q at package scope; the only package-scope declaration may be the RPC's own test func", ident)
		}
	}
}

// TestScaffoldTests_ImportEveryPackageTheyImport is a compile check the
// unit suite can actually run.
//
// Splitting one file into N moved code around imports, and the first cut
// shipped `admin "…/internal/handlers/admin"` in a file that no longer
// named a single type from it — `imported and not used`, i.e. every born
// scaffold test failed to compile, in a repo where nothing type-checks a
// rendered template. go/parser is happy with an unused import, so parsing
// is not enough; this walks each import and demands a reference.
func TestScaffoldTests_ImportEveryPackageTheyImport(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "internal", "handlers", "admin")
	if err := GenerateServiceStub(scaffoldSplitService(), targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if !IsScaffoldTestFile(e.Name()) {
			continue
		}
		checked++
		path := filepath.Join(targetDir, e.Name())
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", e.Name(), perr)
		}
		used := referencedPackageIdents(f)
		for _, imp := range f.Imports {
			name := importLocalName(imp)
			if name == "_" || name == "." {
				continue
			}
			if !used[name] {
				t.Errorf("%s imports %s as %q and never uses it — the file will not compile",
					e.Name(), imp.Path.Value, name)
			}
		}
	}
	// A loop over nothing passes; make sure it looped.
	if checked != 3 {
		t.Fatalf("checked %d scaffold tests, want 3", checked)
	}
}

// importLocalName resolves the identifier an import is referenced by:
// the explicit alias, else the last path segment (which is the right
// answer for every import this template emits).
func importLocalName(imp *ast.ImportSpec) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	path := strings.Trim(imp.Path.Value, `"`)
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// referencedPackageIdents collects the X of every `X.Sel` selector in the
// file — the set of package names the body actually reaches for.
func referencedPackageIdents(f *ast.File) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

// TestScaffoldTests_NeverReofferAnImplementedRPC pins the ownership rule
// that replaced the placeholder-marker mechanism.
//
// The scaffold row asserts CodeUnimplemented. Re-offering it for an RPC
// somebody has already implemented would hand them a permanently red suite,
// so the file is born exactly once — beside the stub — and never again.
func TestScaffoldTests_NeverReofferAnImplementedRPC(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "admin")
	svc := scaffoldSplitService()
	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}

	// The owner implements ShipOrder, rewrites its rows, then decides the
	// coverage belongs in a file they named themselves and deletes ours.
	shipTest := filepath.Join(targetDir, ScaffoldTestFileName("ShipOrder"))
	if err := os.Remove(shipTest); err != nil {
		t.Fatalf("remove %s: %v", shipTest, err)
	}

	if _, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, nil, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}

	if _, err := os.Stat(shipTest); err == nil {
		t.Error("forge re-birthed a scaffold test for an already-implemented RPC — its CodeUnimplemented row would be red forever")
	}

	// A genuinely NEW RPC does get its file, in the same pass that stubs it.
	svc.Methods = append(svc.Methods, Method{
		Name: "CancelOrder", InputType: "CancelOrderRequest", OutputType: "CancelOrderResponse",
	})
	if _, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, nil, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs (new RPC): %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, ScaffoldTestFileName("CancelOrder"))); err != nil {
		t.Errorf("a newly stubbed RPC must get its scaffold test: %v", err)
	}
}

// TestIsScaffoldTestFile keeps the one definition of "which files are
// forge's born scaffold tests" honest, including the pre-split filename
// still sitting in real projects.
func TestIsScaffoldTestFile(t *testing.T) {
	yes := []string{
		"handlers_scaffold_test.go", // born before the per-RPC split
		"handlers_scaffold_ship_order_test.go",
		"handlers_scaffold_echo_test.go",
	}
	no := []string{
		"handlers_test.go",      // the canonical slot, always the user's
		"handlers_crud_test.go", // owns the CRUD surface, different mechanism
		"handlers_scaffold_ship_order.go",
		"service.go",
	}
	for _, n := range yes {
		if !IsScaffoldTestFile(n) {
			t.Errorf("IsScaffoldTestFile(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if IsScaffoldTestFile(n) {
			t.Errorf("IsScaffoldTestFile(%q) = true, want false", n)
		}
	}

	for rpc, want := range map[string]string{
		"ShipOrder":          "handlers_scaffold_ship_order_test.go",
		"SubmitPrescription": "handlers_scaffold_submit_prescription_test.go",
		"Echo":               "handlers_scaffold_echo_test.go",
	} {
		if got := ScaffoldTestFileName(rpc); got != want {
			t.Errorf("ScaffoldTestFileName(%q) = %q, want %q", rpc, got, want)
		}
		if !IsScaffoldTestFile(want) {
			t.Errorf("%s is not recognised by its own predicate", want)
		}
	}
}

// topLevelDeclNames lists every identifier a file declares at package
// scope: funcs (including methods, named by receiver.Name), types, vars and
// consts.
func topLevelDeclNames(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if decl.Recv != nil {
				out = append(out, fmt.Sprintf("method %s", decl.Name.Name))
				continue
			}
			out = append(out, decl.Name.Name)
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					out = append(out, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						out = append(out, n.Name)
					}
				}
			}
		}
	}
	return out
}
