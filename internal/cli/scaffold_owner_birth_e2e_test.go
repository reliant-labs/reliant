//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EScaffoldOwnerDeclaredAtBirth is the gate for `// forge:owner` on a
// proto field.
//
// The owner-scoping wrapper in handlers_crud.go is scaffolded only when that
// file is born, and bare `forge scaffold` births the migration and generates
// the file in one run. With the owner declarable only by a later migration,
// the normal path always produced an unscoped file and ~100 wrappers to
// paste from `forge project audit`. Declared on the field, the birth
// migration carries the COMMENT ON COLUMN, and the one run births:
//
//   - every authenticated CRUD rpc over the table scoped to the column;
//   - a Create that stamps a `forge:read-only` NOT NULL owner column (which
//     used to fail generate as "can never be populated");
//   - for a table owned by itself (the marker on `id`), an Update that
//     predicates on the key and never stamps it.
func TestE2EScaffoldOwnerDeclaredAtBirth(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "ownapp", "--mod", "example.com/ownapp", "--service", "agents")
	projectDir := filepath.Join(dir, "ownapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "agents", "v1", "agents.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
// forge:entity
message AgentRun {
  string id = 1;
  // forge:read-only forge:owner
  string tenant_id = 2;
  string name = 3;
}

// forge:entity
message Tenant {
  string id = 1; // forge:owner
  string slug = 2;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author agents proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	if up := readBornMigrationE2E(t, projectDir, "agent_runs"); !strings.Contains(up, "COMMENT ON COLUMN agent_runs.tenant_id IS 'forge:owner';") {
		t.Errorf("agent_runs birth migration must declare its owner column:\n%s", up)
	}
	if up := readBornMigrationE2E(t, projectDir, "tenants"); !strings.Contains(up, "COMMENT ON COLUMN tenants.id IS 'forge:owner';") {
		t.Errorf("tenants birth migration must declare its key as the owner column:\n%s", up)
	}

	shim := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "agents", "handlers_crud.go"))
	for _, want := range []string{
		`orm.WhereEq("tenant_id", owner)`,
		"e.TenantID = owner",
		`orm.WhereEq("id", owner)`,
		"Auth: SCOPED to tenant_id (forge:owner)",
		"Auth: SCOPED to id (forge:owner)",
	} {
		if !strings.Contains(shim, want) {
			t.Errorf("handlers_crud.go must be BORN scoped (%q):\n%s", want, shim)
		}
	}
	if strings.Contains(shim, "AUTHENTICATED, UNSCOPED") {
		t.Errorf("no CRUD rpc over an owned table may be born unscoped:\n%s", shim)
	}
	update := shim[strings.Index(shim, "func (s *Service) UpdateTenant("):]
	if end := strings.Index(update[1:], "\nfunc "); end >= 0 {
		update = update[:end+1]
	}
	if strings.Contains(update, "e.ID = owner") {
		t.Errorf("UpdateTenant must never stamp the key it is owned by:\n%s", update)
	}

	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/...")
}
