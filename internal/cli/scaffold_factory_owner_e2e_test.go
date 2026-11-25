//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EFactoryOwnerSeedsParentsUnderTheCaller is the gate for
// FactoryOwner on the generated create-request factories.
//
// A scoped Create stamps the row's owner from the caller's claims; the
// factory seeded the row's parents under owners the seed planner invented.
// So with a tenants table every scoped create in the scaffolded lifecycle
// test referenced a tenant that did not exist, and with a same-tenant guard
// (a trigger, the standard stand-in for a composite (tenant_id, id) foreign
// key) the child crossed tenants. This runs the generated app's own
// lifecycle tests over exactly that schema.
func TestE2EFactoryOwnerSeedsParentsUnderTheCaller(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "tenantapp", "--mod", "example.com/tenantapp", "--service", "growth")
	projectDir := filepath.Join(dir, "tenantapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "growth", "v1", "growth.proto")
	proto := readFileE2E(t, protoPath) + `
// forge:entity
message Tenant {
  string id = 1;
  string slug = 2;
}

// forge:entity
message Strategy {
  string id = 1;
  string tenant_id = 2;
  string name = 3;
}

// forge:entity
message Hypothesis {
  string id = 1;
  string tenant_id = 2;
  string strategy_id = 3;
  string claim = 4;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author growth proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	// Declare the owners and the guard, then let the handler and lifecycle
	// test files be born again after the declaration, so both are scoped.
	guard := `COMMENT ON COLUMN strategies.tenant_id IS 'forge:owner';
COMMENT ON COLUMN hypotheses.tenant_id IS 'forge:owner';
CREATE FUNCTION enforce_same_tenant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM strategies s WHERE s.id = NEW.strategy_id AND s.tenant_id = NEW.tenant_id) THEN
        RAISE EXCEPTION 'hypothesis % crosses tenants', NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER hypotheses_same_tenant BEFORE INSERT OR UPDATE ON hypotheses
    FOR EACH ROW EXECUTE FUNCTION enforce_same_tenant();
`
	if err := os.WriteFile(filepath.Join(projectDir, "db", "migrations", "99999999999999_tenant_guard.up.sql"), []byte(guard), 0o644); err != nil {
		t.Fatalf("write guard migration: %v", err)
	}
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "growth")
	rescaffold := []string{"project", "rescaffold"}
	for _, f := range []string{"handlers_crud.go", "handlers_crud_test.go"} {
		if err := os.Remove(filepath.Join(handlerDir, f)); err != nil {
			t.Fatalf("remove %s: %v", f, err)
		}
		rescaffold = append(rescaffold, filepath.Join("internal", "handlers", "growth", f))
	}
	runCmd(t, projectDir, forgeBin, rescaffold...)
	runCmd(t, projectDir, forgeBin, "generate")

	factories := readFileE2E(t, filepath.Join(handlerDir, "factories_gen_test.go"))
	for _, want := range []string{
		"func NewCreateHypothesisRequest(t testing.TB, database orm.Context, variant int, opts ...FactoryOption)",
		"func NewCreateStrategyRequest(t testing.TB, database orm.Context, variant int, opts ...FactoryOption)",
		"func NewCreateTenantRequest(t testing.TB, database orm.Context, variant int) *pb.CreateTenantRequest",
		"func FactoryOwner(owner string) FactoryOption",
	} {
		if !strings.Contains(factories, want) {
			t.Errorf("factories_gen_test.go missing %q:\n%s", want, factories)
		}
	}
	lifecycle := readFileE2E(t, filepath.Join(handlerDir, "handlers_crud_test.go"))
	if !strings.Contains(lifecycle, `growth.NewCreateHypothesisRequest(t, db, 0, growth.FactoryOwner("test-user"))`) {
		t.Errorf("the scoped lifecycle test must seed parents under the owner its handler stamps:\n%s", lifecycle)
	}

	// The generated app's own lifecycle tests: every scoped create lands.
	runCmd(t, projectDir, "go", "test", "./internal/handlers/growth/")
}
