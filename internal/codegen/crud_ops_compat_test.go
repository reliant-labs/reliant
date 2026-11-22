package codegen

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

var updateCRUDOpsCompat = flag.Bool("update-crud-ops-compat", false, "rewrite testdata/crud_ops_compat/*.golden from the current renders")

// TestCRUDOps_DBShapeRendersUnchanged pins the CRUD output of a handler
// whose Deps carries only `DB orm.Context` (or nothing, so forge injects
// it): the shape every project had before a Deps field could name an
// entity's generated store. Routing ops through a declared store is
// additive, so none of these files may change by a byte. The goldens were
// recorded from main before that routing existed; a diff here is a
// regeneration diff every existing project would see.
func TestCRUDOps_DBShapeRendersUnchanged(t *testing.T) {
	read := func(t *testing.T, dir string, names ...string) map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, n := range names {
			out[n] = readTestFile(t, filepath.Join(dir, n))
		}
		return out
	}
	generate := func(t *testing.T, projectDir string, svc ServiceDef, entities []EntityDef) {
		t.Helper()
		if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
			t.Fatalf("GenerateCRUDHandlers: %v", err)
		}
	}
	files := []string{"handlers_crud_ops_gen.go", "handlers_crud.go", "service.go"}

	cases := []struct {
		name string
		run  func(t *testing.T) map[string]string
	}{
		{"crews_owned", func(t *testing.T) map[string]string {
			projectDir, handlerDir := ownerTestHandlerDir(t)
			svc, entities := ownerTestServiceAndEntity(true, true)
			generate(t, projectDir, svc, entities)
			return read(t, handlerDir, files...)
		}},
		{"crews_unowned_public", func(t *testing.T) map[string]string {
			projectDir, handlerDir := ownerTestHandlerDir(t)
			svc, entities := ownerTestServiceAndEntity(false, false)
			generate(t, projectDir, svc, entities)
			return read(t, handlerDir, files...)
		}},
		{"crews_tenant", func(t *testing.T) map[string]string {
			projectDir, handlerDir := ownerTestHandlerDir(t)
			svc, entities := tenantServiceAndEntity()
			generate(t, projectDir, svc, entities)
			return read(t, handlerDir, files...)
		}},
		{"crews_declared_owner", func(t *testing.T) map[string]string {
			ops, shim, service := generateOwnedCRUD(t, []schemadef.ResolvedOwnerTerm{
				{Kind: "user", Column: "company_id"},
				{Kind: "org", Column: "org_id"},
			}, false)
			return map[string]string{"handlers_crud_ops_gen.go": ops, "handlers_crud.go": shim, "service.go": service}
		}},
		{"crews_relocated_orm", func(t *testing.T) map[string]string {
			projectDir, handlerDir := storeProject(t)
			svc, entities := ownerTestServiceAndEntity(true, true)
			generate(t, projectDir, svc, entities)
			return read(t, handlerDir, files...)
		}},
		{"crews_deps_without_db", func(t *testing.T) map[string]string {
			// No DB field yet: forge injects `DB orm.Context` and its
			// validateDeps check, exactly as before.
			projectDir, handlerDir := ownerTestHandlerDir(t)
			serviceGo := "package crews\n\nimport (\n\t\"fmt\"\n)\n\ntype Deps struct {\n\t// Add your dependencies here.\n}\n\nfunc (d Deps) validateDeps() error {\n\t// Add checks for your required Deps fields here.\n\treturn fmt.Errorf(\"x\")\n}\n\ntype Service struct {\n\tdeps Deps\n}\n"
			if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
				t.Fatal(err)
			}
			svc, entities := ownerTestServiceAndEntity(false, true)
			generate(t, projectDir, svc, entities)
			return read(t, handlerDir, files...)
		}},
		{"patients_masked", func(t *testing.T) map[string]string {
			projectDir := t.TempDir()
			handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
			if err := os.MkdirAll(handlerDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeMaskTestServiceGo(t, handlerDir)
			generate(t, projectDir, maskTestService(true), maskTestEntities())
			return read(t, handlerDir, files...)
		}},
		{"tasks_int64_pk", func(t *testing.T) map[string]string {
			ops, _ := renderTaskOps(t, "int64", "int64")
			return map[string]string{"handlers_crud_ops_gen.go": ops}
		}},
		{"tasks_string_pk", func(t *testing.T) map[string]string {
			ops, _ := renderTaskOps(t, "string", "string")
			return map[string]string{"handlers_crud_ops_gen.go": ops}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.run(t)
			for name, content := range got {
				golden := filepath.Join("testdata", "crud_ops_compat", c.name, name+".golden")
				if *updateCRUDOpsCompat {
					if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("read golden %s (run with -update-crud-ops-compat on main to record it): %v", golden, err)
				}
				if string(want) != content {
					t.Errorf("%s changed for a DB-only Deps — existing projects would see a regeneration diff.\n--- want (%s)\n%s\n--- got\n%s", name, golden, want, content)
				}
			}
		})
	}
}
