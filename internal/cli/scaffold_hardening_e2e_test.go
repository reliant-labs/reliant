//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestE2EScaffoldSchemaHardeningDefaults is the real-pipeline gate for the
// four schema-hardening markers. It scaffolds one project carrying all four
// and asserts, per feature, that:
//
//   - the SCHEMA lands in the USER-OWNED birth migration (composite indexes,
//     the append-only DB guard, the opt-in deleted_at column, the secret
//     column) — never a forge-owned _gen file, and
//   - the runtime BEHAVIOR rides the EXISTING projection (no Update/Delete op
//     for the append-only entity, the secret field skipped on the read path
//     but writable, soft delete wired from the deleted_at column), and
//   - the generated app builds + vets, and generate is idempotent.
func TestE2EScaffoldSchemaHardeningDefaults(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "shopapp", "--mod", "example.com/shopapp", "--service", "shop")
	projectDir := filepath.Join(dir, "shopapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "shop", "v1", "shop.proto")
	proto := readFileE2E(t, protoPath)
	proto = strings.Replace(proto, "  // TODO: Add your RPC methods here.",
		"  // RPCs completed by scaffold from the markers below.", 1)
	proto += `
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_PENDING = 1;
  ORDER_STATUS_SHIPPED = 2;
}

// forge:entity
message Order {
  string id = 1;
  string region = 2;
  OrderStatus status = 3;
  double amount = 4;
}

message ListOrdersRequest {
  int32 page_size = 1;
  string page_token = 2;
  optional OrderStatus status = 3;
  string order_by = 4;
  bool descending = 5;
}

// forge:append-only
message AuditLog {
  string id = 1;
  string region = 2;
  string action = 3;
}

// forge:entity
message Credential {
  string id = 1;
  string region = 2;
  string name = 3;
  // forge:secret
  string secret_token = 4;
}

// forge:soft-delete
message Session {
  string id = 1;
  string region = 2;
  string device = 3;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	migDir := filepath.Join(projectDir, "db", "migrations")

	// ── Feature 2: append-only (AuditLog) ────────────────────────────
	auditUp := readMigration(t, migDir, "create_audit_logs.up.sql")
	if !strings.Contains(auditUp, "CREATE TRIGGER audit_logs_append_only") ||
		!strings.Contains(auditUp, "RAISE EXCEPTION 'table audit_logs is append-only") {
		t.Errorf("audit_logs migration missing the append-only guard:\n%s", auditUp)
	}
	if downs, _ := filepath.Glob(filepath.Join(migDir, "*.down.sql")); len(downs) != 0 {
		t.Errorf("scaffold must write no down migrations (forge rolls forward only), found %v", downs)
	}
	// No Update/Delete surface anywhere.
	protoAfter := readFileE2E(t, protoPath)
	for _, forbidden := range []string{"UpdateAuditLog", "DeleteAuditLog"} {
		if strings.Contains(protoAfter, forbidden) {
			t.Errorf("append-only entity must have no %s in the proto", forbidden)
		}
	}
	ops := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "shop", "handlers_crud_ops_gen.go"))
	for _, forbidden := range []string{"crudUpdateAuditLogOp", "crudDeleteAuditLogOp"} {
		if strings.Contains(ops, forbidden) {
			t.Errorf("append-only entity must have no %s in generated ops", forbidden)
		}
	}
	if !strings.Contains(ops, "crudCreateAuditLogOp") || !strings.Contains(ops, "crudListAuditLogsOp") {
		t.Error("append-only entity must still have Create/List ops")
	}

	// ── Feature 3: secret field (Credential) ─────────────────────────
	credUp := readMigration(t, migDir, "create_credentials.up.sql")
	if !strings.Contains(credUp, "secret_token TEXT NOT NULL DEFAULT ''") {
		t.Errorf("secret field must still be a column in the migration:\n%s", credUp)
	}
	toProto := sliceFunc(t, ops, "func credentialToProto")
	if strings.Contains(toProto, "m.SecretToken =") {
		t.Errorf("secret field must NOT be packed on the read path:\n%s", toProto)
	}
	fromProto := sliceFunc(t, ops, "func credentialFromProto")
	if !strings.Contains(fromProto, "e.SecretToken = m.SecretToken") {
		t.Errorf("secret field must stay writable (fromProto):\n%s", fromProto)
	}
	if !strings.Contains(ops, "e.SecretToken = req.SecretToken") {
		t.Error("secret field must be settable on create")
	}
	// The list search filter must not span the secret column.
	if strings.Contains(ops, `[]string{"name", "secret_token"}`) {
		t.Error("secret column must be excluded from the list search span")
	}

	// ── Feature 4: soft-delete (Session, opt-in) ─────────────────────
	sessUp := readMigration(t, migDir, "create_sessions.up.sql")
	if !strings.Contains(sessUp, "deleted_at TIMESTAMPTZ") {
		t.Errorf("soft-delete entity must have deleted_at:\n%s", sessUp)
	}
	// Unmarked entities have NO deleted_at.
	ordersUp := readMigration(t, migDir, "create_orders.up.sql")
	if strings.Contains(ordersUp, "deleted_at") {
		t.Errorf("unmarked entity (orders) must NOT get deleted_at:\n%s", ordersUp)
	}
	sessOrm := readFileE2E(t, filepath.Join(projectDir, "internal", "db", "session_orm_gen.go"))
	if !strings.Contains(sessOrm, "soft_delete") {
		t.Error("session ORM must carry Bun's soft_delete tag")
	}

	// ── Ownership: schema lives in user-owned migrations, no _gen DDL ──
	assertMigrationsUserOwned(t, migDir)
	assertNoForgeOwnedSchemaDDL(t, projectDir)

	// ── Generated app builds + vets ──────────────────────────────────
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")

	// ── Idempotency: generate ×2 byte-identical ──────────────────────
	before := hashProjectTree(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")
	after := hashProjectTree(t, projectDir)
	var diffs []string
	for p, h := range after {
		if before[p] != h {
			diffs = append(diffs, p)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Fatalf("generate is not idempotent — changed files:\n  %s", strings.Join(diffs, "\n  "))
	}
}

// readMigration reads the NNNNN_<suffix> migration file (the number prefix
// is assigned by birth order, so match on the suffix).
func readMigration(t *testing.T, migDir, suffix string) string {
	t.Helper()
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			return readFileE2E(t, filepath.Join(migDir, e.Name()))
		}
	}
	t.Fatalf("no migration ending in %q under %s", suffix, migDir)
	return ""
}

// sliceFunc returns the source of the named Go func through its closing
// brace at column 0 (the generated funcs are gofmt'd, so `\n}` terminates).
func sliceFunc(t *testing.T, src, header string) string {
	t.Helper()
	i := strings.Index(src, header)
	if i < 0 {
		t.Fatalf("func %q not found", header)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j+2]
	}
	return rest
}

// assertMigrationsUserOwned pins that every birth migration is user-owned
// scaffold-once (the "YOURS from birth" banner, never a forge-owned
// DO-NOT-EDIT header).
func assertMigrationsUserOwned(t *testing.T, migDir string) {
	t.Helper()
	ups, _ := filepath.Glob(filepath.Join(migDir, "*.up.sql"))
	for _, p := range ups {
		body := readFileE2E(t, p)
		if strings.Contains(body, "DO NOT EDIT") || strings.Contains(body, "forge-owned") {
			t.Errorf("birth migration %s carries a forge-owned header — must be user-owned", filepath.Base(p))
		}
		if !strings.Contains(body, "YOURS from birth") {
			t.Errorf("birth migration %s missing the user-owned banner", filepath.Base(p))
		}
	}
}

// assertNoForgeOwnedSchemaDDL pins the ownership rule: no forge-owned (_gen /
// DO-NOT-EDIT) file may carry the hardening SCHEMA — the columns, indexes and
// DB guard all live in the user-owned migrations. This is what proves these
// features introduced NO new forge-owned _gen schema surface.
func assertNoForgeOwnedSchemaDDL(t *testing.T, projectDir string) {
	t.Helper()
	ddl := []string{"CREATE TRIGGER", "forbid_mutation", "CREATE INDEX ", "deleted_at TIMESTAMPTZ"}
	_ = filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "/.forge-pkg/") || strings.Contains(path, "/pkg/") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		s := string(body)
		if !strings.Contains(s, "DO NOT EDIT") {
			return nil
		}
		for _, d := range ddl {
			if strings.Contains(s, d) {
				t.Errorf("forge-owned file %s carries schema DDL %q — the schema must live only in user-owned migrations", path, d)
			}
		}
		return nil
	})
}
