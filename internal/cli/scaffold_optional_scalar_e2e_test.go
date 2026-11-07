//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/migrationver"
)

// TestE2EOptionalScalarEntityBirthAndConversion is the acceptance gate
// for proto3 `optional` scalars on entities, end to end — the db
// skill's contract "optional maps to a nullable column" must hold at
// BOTH ends of the vertical:
//
//   - birth: `// forge:entity` + `forge scaffold` births the
//     optional scalar as a NULLABLE column — including when the message
//     body is authored on ONE line (the raw scanner used to capture
//     zero fields from inline bodies and silently birth a column-less
//     table);
//   - projection: the generated proto<->entity conversions pair the
//     *string wire field (explicit presence) with the *string entity
//     field (nullable column) nil-safely. The old code treated the
//     wire side as a plain scalar and emitted `e.X = &v` (**string)
//     and `m.X = *e.X` (string into *string) — the project did not
//     compile.
//
// The entity carries the full field mix the contract names: a plain
// `string` (NOT NULL + zero default), an `optional string` (nullable),
// an `optional google.protobuf.Timestamp` (TIMESTAMPTZ, nullable —
// already worked; regression pin), and a `repeated string` (TEXT[]).
// Runs the REAL pipeline (buf compile + embedded postgres shadow-apply)
// and requires generate x2, build, vet all green and idempotent.
func TestE2EOptionalScalarEntityBirthAndConversion(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "optapp", "--mod", "example.com/optapp", "--service", "orders")
	projectDir := filepath.Join(dir, "optapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author two marked entities in the service proto:
	//   - Order, multi-line, with the full contract field mix;
	//   - Widget, body on ONE LINE (the shape that used to silently
	//     birth column-less: the raw scan captured no fields).
	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto = strings.Replace(proto, `import "forge/v1/forge.proto";`,
		"import \"forge/v1/forge.proto\";\nimport \"google/protobuf/timestamp.proto\";", 1)
	proto += `
// forge:entity
message Order {
  string id = 1;
  string name = 2;
  optional string failure_reason = 3;
  optional google.protobuf.Timestamp completed_at = 4;
  repeated string tags = 5;
}

// forge:entity
message Widget { string id = 1; string name = 2; optional string nickname = 3; }
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author orders proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── birth: the born migrations honor the optional -> nullable map ──
	ordersUp := readBornMigrationE2E(t, projectDir, "orders")
	for _, want := range []string{
		"failure_reason TEXT,",              // optional string -> nullable, no default
		"name TEXT NOT NULL DEFAULT ''",     // plain string -> NOT NULL + zero default
		"completed_at TIMESTAMPTZ,",         // optional Timestamp -> nullable TIMESTAMPTZ
		"tags TEXT[] NOT NULL DEFAULT '{}'", // repeated string -> array
	} {
		if !strings.Contains(ordersUp, want) {
			t.Errorf("orders birth migration should contain %q; got:\n%s", want, ordersUp)
		}
	}
	for _, banned := range []string{"failure_reason TEXT NOT NULL", "completed_at TIMESTAMPTZ NOT NULL"} {
		if strings.Contains(ordersUp, banned) {
			t.Errorf("orders birth migration must not contain %q (optional means NULLABLE):\n%s", banned, ordersUp)
		}
	}

	widgetsUp := readBornMigrationE2E(t, projectDir, "widgets")
	if !strings.Contains(widgetsUp, "nickname TEXT,") || strings.Contains(widgetsUp, "nickname TEXT NOT NULL") {
		t.Errorf("inline-body optional scalar must birth a nullable column; widgets migration:\n%s", widgetsUp)
	}
	if !strings.Contains(widgetsUp, "name TEXT NOT NULL DEFAULT ''") {
		t.Errorf("inline-body plain scalar must birth NOT NULL + zero default; widgets migration:\n%s", widgetsUp)
	}

	// ── projection: conversions pair pointer to pointer, nil-safely ──
	ops := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "orders", "handlers_crud_ops_gen.go"))
	if !strings.Contains(ops, "if e.FailureReason != nil {") || !strings.Contains(ops, "if m.FailureReason != nil {") {
		t.Errorf("optional scalar conversions should be nil-guarded pointer copies in both directions; ops:\n%s", ops)
	}
	for _, banned := range []string{
		"Nickname: wire-only field",      // the silently-dropped column would degrade to wire-only
		"FailureReason: wire-only field", // ditto (belt and suspenders)
	} {
		if strings.Contains(ops, banned) {
			t.Errorf("ops file degraded a stored field to wire-only (%q):\n%s", banned, ops)
		}
	}

	// ── generate x2: green and byte-for-byte idempotent ──
	snaps := make([]map[string]string, 0, 2)
	for i := 0; i < 2; i++ {
		runCmd(t, projectDir, forgeBin, "generate")
		snaps = append(snaps, hashProjectTree(t, projectDir))
		runCmd(t, projectDir, "go", "build", "./...")
		runCmd(t, projectDir, "go", "vet", "./...")
	}
	if diff := diffTreeE2E(snaps[0], snaps[1]); diff != "" {
		t.Errorf("generate #2 is not idempotent vs #1 (file churn):\n%s", diff)
	}
}

// readBornMigrationE2E reads the NNNNN_create_<table>.up.sql migration
// the birth wrote.
func readBornMigrationE2E(t *testing.T, projectDir, table string) string {
	t.Helper()
	return readFileE2E(t, bornMigrationPathE2E(t, projectDir, table))
}

// bornMigrationPathE2E locates the migration `forge scaffold` wrote to create
// table, by glob rather than by a literal version prefix.
//
// Scaffolded migrations carry a 14-digit UTC timestamp version
// (internal/migrationver), allocated at the instant the scaffold runs, so no
// test can name one as a literal — and a test that tried would be asserting
// on the clock.
func bornMigrationPathE2E(t *testing.T, projectDir, table string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(projectDir, "db", "migrations", "*_create_"+table+".up.sql"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one create-%s migration, got %v (err %v)", table, matches, err)
	}
	return matches[0]
}

// nextMigrationPathE2E allocates the path for a migration the TEST adds by
// hand, the same way `forge db migration new` allocates it.
//
// The allocation matters for ordering, not just for naming. A hand-written
// migration in these fixtures almost always depends on a table the birth
// migration created, so it must sort AFTER birth — and birth's timestamp
// version is nine orders of magnitude larger than any 5-digit sequence
// number. A literal "00002_" sorts FIRST, and the migration runs against a
// table that does not exist yet.
func nextMigrationPathE2E(t *testing.T, projectDir, stem string) string {
	t.Helper()
	migDir := filepath.Join(projectDir, "db", "migrations")
	version, err := migrationver.Next(migDir)
	if err != nil {
		t.Fatalf("allocate migration version for %s: %v", stem, err)
	}
	return filepath.Join(migDir, version+"_"+stem+".up.sql")
}
