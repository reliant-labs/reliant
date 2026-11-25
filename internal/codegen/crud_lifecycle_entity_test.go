package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lifecycleEntitySvc is PatientsService with a Create + Get pair for each
// named entity, and the matched CRUD methods over string-PK entities.
func lifecycleEntitySvc(entities ...string) (ServiceDef, []CRUDMethod) {
	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
	}
	var defs []EntityDef
	for _, e := range entities {
		svc.Methods = append(svc.Methods,
			Method{Name: "Create" + e, InputType: "Create" + e + "Request", OutputType: "Create" + e + "Response"},
			Method{Name: "Get" + e, InputType: "Get" + e + "Request", OutputType: "Get" + e + "Response"},
		)
		defs = append(defs, EntityDef{
			Name: e, TableName: strings.ToLower(e) + "s", PkField: "id", PkGoType: "string",
			Fields: []EntityField{
				{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			},
		})
	}
	return svc, MatchCRUDMethods(svc, defs)
}

// lifecycleEntityProject is a project with the patients handler package and,
// when migrated, an SQL migration (so the harness declares NewMigratedTestDB).
func lifecycleEntityProject(t *testing.T, migrated bool) (projectDir, handlerDir string) {
	t.Helper()
	projectDir = t.TempDir()
	handlerDir = filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if migrated {
		migDir := filepath.Join(projectDir, "db", "migrations")
		if err := os.MkdirAll(migDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(migDir, "20260101000000_init.up.sql"), []byte("CREATE TABLE patients (id TEXT PRIMARY KEY);\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projectDir, handlerDir
}

func generateLifecycle(t *testing.T, projectDir string, entities ...string) CRUDResult {
	t.Helper()
	svc, methods := lifecycleEntitySvc(entities...)
	res, err := GenerateCRUDTests(svc, methods, "example.com/test", projectDir, nil)
	if err != nil {
		t.Fatalf("GenerateCRUDTests(%v): %v", entities, err)
	}
	return res
}

func readLifecycleFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), path, body, parser.SkipObjectResolution); err != nil {
		t.Fatalf("%s is not valid Go: %v\n----\n%s", filepath.Base(path), err, body)
	}
	return string(body)
}

// TestGenerateCRUDTests_LaterEntityGetsItsOwnLifecycleFile: an entity the
// service gains after handlers_crud_test.go was born gets a lifecycle test
// of its own, beside the user's file and compiling with it, and a deletion
// of that file is final.
func TestGenerateCRUDTests_LaterEntityGetsItsOwnLifecycleFile(t *testing.T) {
	projectDir, handlerDir := lifecycleEntityProject(t, true)
	mainPath := filepath.Join(handlerDir, "handlers_crud_test.go")
	generateLifecycle(t, projectDir, "Patient")
	mainBefore := readLifecycleFile(t, mainPath)
	// crudTestDB and crudTestCtx stay: they are the seams an author's own
	// tests in this file reach for.
	for _, want := range []string{"func TestCRUD_Patient_Lifecycle(", "func crudTestDB(", "return patients.NewMigratedTestDB(t)", "func crudTestCtx(", "func assertCleanError("} {
		if !strings.Contains(mainBefore, want) {
			t.Errorf("handlers_crud_test.go misses %q\n----\n%s", want, mainBefore)
		}
	}
	// The migrated template is cloned, never replayed file by file.
	if strings.Contains(mainBefore, "findMigrationsDir") || strings.Contains(mainBefore, "os.ReadFile") {
		t.Errorf("handlers_crud_test.go replays the migrations instead of cloning the migrated template\n----\n%s", mainBefore)
	}

	res := generateLifecycle(t, projectDir, "Patient", "Visit")
	if !res.Wrote {
		t.Errorf("adding Visit wrote nothing: %+v", res)
	}
	visitPath := filepath.Join(handlerDir, "handlers_crud_visit_test.go")
	visit := readLifecycleFile(t, visitPath)
	for _, want := range []string{"func TestCRUD_Visit_Lifecycle(", "patients.NewMigratedTestDB(t)", "patients.AuthedContext(t)"} {
		if !strings.Contains(visit, want) {
			t.Errorf("handlers_crud_visit_test.go misses %q\n----\n%s", want, visit)
		}
	}
	// It must compile beside handlers_crud_test.go: no second Patient test,
	// no second helper, and no import only those helpers used.
	for _, dup := range []string{"TestCRUD_Patient_Lifecycle", "func assertCleanError(", "func crudTestDB(", "func crudTestCtx(", "pkg/svcerr", "pkg/middleware", `"strings"`, `"context"`} {
		if strings.Contains(visit, dup) {
			t.Errorf("handlers_crud_visit_test.go repeats %q from handlers_crud_test.go\n----\n%s", dup, visit)
		}
	}
	if got := readLifecycleFile(t, mainPath); got != mainBefore {
		t.Error("handlers_crud_test.go was rewritten; it is the user's")
	}

	// Covered now: a rerun writes nothing.
	if res := generateLifecycle(t, projectDir, "Patient", "Visit"); res.Wrote {
		t.Errorf("a rerun with every entity covered wrote again: %+v", res)
	}
	// Deleted: it stays deleted.
	if err := os.Remove(visitPath); err != nil {
		t.Fatal(err)
	}
	generateLifecycle(t, projectDir, "Patient", "Visit")
	if _, err := os.Stat(visitPath); err == nil {
		t.Error("handlers_crud_visit_test.go came back after the user deleted it")
	}
}

// TestGenerateCRUDTests_LaterEntityDeclaresTheHelpersThePackageLacks: when
// the user's handlers_crud_test.go no longer declares assertCleanError (or
// the replay helpers of a project without migrations), the per-entity file
// declares them itself, once per run.
func TestGenerateCRUDTests_LaterEntityDeclaresTheHelpersThePackageLacks(t *testing.T) {
	projectDir, handlerDir := lifecycleEntityProject(t, false)
	generateLifecycle(t, projectDir, "Patient")
	mainPath := filepath.Join(handlerDir, "handlers_crud_test.go")
	if main := readLifecycleFile(t, mainPath); !strings.Contains(main, "func crudTestDB(") {
		t.Fatalf("without migrations handlers_crud_test.go should replay them through crudTestDB\n----\n%s", main)
	}
	// The user rewrote it with helpers of their own names.
	own := "package patients_test\n\nimport \"testing\"\n\nfunc TestCRUD_Patient_Lifecycle(t *testing.T) {}\n"
	if err := os.WriteFile(mainPath, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}

	generateLifecycle(t, projectDir, "Patient", "Visit", "Ward")
	visit := readLifecycleFile(t, filepath.Join(handlerDir, "handlers_crud_visit_test.go"))
	ward := readLifecycleFile(t, filepath.Join(handlerDir, "handlers_crud_ward_test.go"))
	for _, helper := range []string{"func assertCleanError(", "func crudTestDB(", "func findMigrationsDir("} {
		if n := strings.Count(visit, helper) + strings.Count(ward, helper); n != 1 {
			t.Errorf("%s declared %d times across the two new files, want exactly once", helper, n)
		}
	}
}

// TestGenerateCRUDTests_DeletedLifecycleFileOptsTheServiceOut: deleting
// handlers_crud_test.go is the user's decision for the whole service. Its
// entities must not come back one file at a time, and a later entity gets
// none either.
func TestGenerateCRUDTests_DeletedLifecycleFileOptsTheServiceOut(t *testing.T) {
	projectDir, handlerDir := lifecycleEntityProject(t, true)
	generateLifecycle(t, projectDir, "Patient")
	if err := os.Remove(filepath.Join(handlerDir, "handlers_crud_test.go")); err != nil {
		t.Fatal(err)
	}
	if res := generateLifecycle(t, projectDir, "Patient", "Visit"); res.Wrote {
		t.Errorf("wrote a lifecycle test into a service whose handlers_crud_test.go was deleted: %+v", res)
	}
	entries, err := os.ReadDir(handlerDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "handlers_crud_") {
			t.Errorf("%s was written after the user deleted handlers_crud_test.go", e.Name())
		}
	}
}

// TestGenerateCRUDTests_UnparsableTestFileWritesNoCompanion: a _test.go file
// that does not parse hides its declarations, so whether the later entity is
// covered, or a helper declared, is unknown. Nothing is written until it
// parses.
func TestGenerateCRUDTests_UnparsableTestFileWritesNoCompanion(t *testing.T) {
	projectDir, handlerDir := lifecycleEntityProject(t, true)
	generateLifecycle(t, projectDir, "Patient")
	if err := os.WriteFile(filepath.Join(handlerDir, "broken_test.go"), []byte("package patients_test\n\nfunc {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := generateLifecycle(t, projectDir, "Patient", "Visit"); res.Wrote {
		t.Errorf("wrote a companion beside an unparsable test file: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(handlerDir, "handlers_crud_visit_test.go")); err == nil {
		t.Error("handlers_crud_visit_test.go was written while broken_test.go does not parse")
	}
}
