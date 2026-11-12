//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EScaffoldReadOnlyFieldOmittedFromCreate is the end-to-end gate for
// the `// forge:read-only` field marker — the INPUT-side mirror of
// `// forge:secret`.
//
// An author marks a non-client-writable field (here `status` on an Order)
// with a leading `// forge:read-only` comment. After `forge scaffold`:
//
//   - the column is real (schema truth — in the born migration) and the field
//     stays on the entity wire message + the read (Get/List) surface, so
//     reads are unaffected;
//   - the field is EXCLUDED from the born CreateOrderRequest (the client must
//     not set it), and the surviving fields renumber contiguously;
//   - the born scaffold-once artifacts are correct FROM THE START: the CRUD
//     lifecycle test (handlers_crud_test.go) never references the field, so an
//     author never has to hand-strip a Create request post-scaffold (which is
//     exactly what would break those born pages/tests).
//
// The whole rendered project then compiles and vets clean. Mirrors the
// corpus-style local replace so it pins CURRENT-tree behavior.
func TestE2EScaffoldReadOnlyFieldOmittedFromCreate(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "ssapp", "--mod", "example.com/ssapp", "--service", "orders")
	projectDir := filepath.Join(dir, "ssapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author an Order entity: a normal field, a `// forge:read-only` field
	// (leading spelling), and the timestamp convention fields.
	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\n" +
		"message Order {\n" +
		"  string id = 1;\n" +
		"  string customer = 2;\n" +
		"  int64 amount = 3;\n" +
		"  // forge:read-only\n" +
		"  string status = 4;\n" +
		"  google.protobuf.Timestamp created_at = 5;\n" +
		"  google.protobuf.Timestamp updated_at = 6;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append Order entity to orders proto: %v", err)
	}

	// One-command flow: birth the marked entity (migration + CRUD quintet),
	// then run the real generate pipeline.
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The column is schema truth — present in the born migration.
	migDir := filepath.Join(projectDir, "db", "migrations")
	migEntries, _ := os.ReadDir(migDir)
	var upMig string
	for _, e := range migEntries {
		if strings.HasSuffix(e.Name(), "_create_orders.up.sql") {
			upMig = readFileE2E(t, filepath.Join(migDir, e.Name()))
		}
	}
	if upMig == "" {
		t.Fatalf("no create_orders migration born; migrations: %v", migEntries)
	}
	if !strings.Contains(upMig, "status") {
		t.Errorf("read-only column must stay in the born migration (schema truth):\n%s", upMig)
	}

	// The born wire contract: the entity message KEEPS status; the
	// CreateOrderRequest OMITS it.
	genProto := readFileE2E(t, protoPath)
	entityMsg := sliceBetweenCLI(genProto, "message Order {", "}")
	if !strings.Contains(entityMsg, "status") {
		t.Errorf("read-only field must stay on the entity wire message:\n%s", entityMsg)
	}
	createReq := sliceBetweenCLI(genProto, "message CreateOrderRequest {", "}")
	if createReq == "" {
		t.Fatalf("CreateOrderRequest was not born into the proto:\n%s", genProto)
	}
	if strings.Contains(createReq, "status") {
		t.Errorf("CreateOrderRequest must OMIT the `// forge:read-only` field:\n%s", createReq)
	}
	for _, keep := range []string{"customer", "amount"} {
		if !strings.Contains(createReq, keep) {
			t.Errorf("CreateOrderRequest dropped a client-settable field %q:\n%s", keep, createReq)
		}
	}

	// The born scaffold-once CRUD lifecycle test must NOT reference the
	// read-only field (neither in the create envelope nor as the
	// masked-update clobber-proof field) — the whole point of the marker.
	crudTestPath := filepath.Join(projectDir, "internal", "handlers", "orders", "handlers_crud_test.go")
	crudTest := readFileE2E(t, crudTestPath)
	if strings.Contains(crudTest, "Status:") || strings.Contains(crudTest, "GetStatus()") || strings.Contains(crudTest, ".Status =") {
		t.Errorf("born handlers_crud_test.go must not reference the read-only field:\n%s", crudTest)
	}
	// Nor may the create-request factory it builds its rows from: the
	// request type has no such field, so setting it would not compile.
	factories := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "orders", "factories_gen_test.go"))
	if strings.Contains(factories, "Status:") {
		t.Errorf("the create-request factory sets the read-only field:\n%s", factories)
	}

	// The rendered project compiles and vets clean.
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}

// sliceBetweenCLI returns the substring of s from the first occurrence of start
// up to (and including) the first occurrence of end after it. Empty if either
// marker is missing.
func sliceBetweenCLI(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return ""
	}
	return s[i : i+j+len(end)]
}
