//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EScaffoldReadOnlySurvivesInlineOptions pins the `// forge:read-only`
// marker against the shape that used to defeat it: a field line that also
// carries an inline `[...]` options bracket.
//
// The marker was read off the field's single physical line, and rejected
// whenever any `<name> = <number>` sat between the field and the comment.
// Every protovalidate bracket contains one — `[(buf.validate.field).int64.gte
// = 0]` reads as "a later field declaration" — so the marker was DROPPED, in
// silence, on exactly the fields that carried validation rules. Across a
// twelve-field app the correlation was exact: every bracketed field kept on
// the born Create request, every unbracketed one stripped. Nothing warned;
// the author believed the field was off the write surface.
//
// The three spellings below are the ones a real proto mixes: bare, bracketed,
// and bracketed with a MULTI-LINE braced value whose `];` lands on a later
// physical line than the field name.
func TestE2EScaffoldReadOnlySurvivesInlineOptions(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "ssiapp", "--mod", "example.com/ssiapp", "--service", "orders")
	projectDir := filepath.Join(dir, "ssiapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\nenum OrderStatus {\n" +
		"  ORDER_STATUS_UNSPECIFIED = 0;\n" +
		"  ORDER_STATUS_OPEN = 1;\n" +
		"  ORDER_STATUS_CLOSED = 2;\n" +
		"}\n" +
		"\n// forge:entity\n" +
		"message Order {\n" +
		"  string customer = 1 [(buf.validate.field).string.min_len = 1];\n" +
		"  int64 unit_price_cents = 2 [(buf.validate.field).int64.gte = 0]; // forge:read-only\n" +
		"  OrderStatus status = 3; // forge:read-only\n" +
		// The multi-line braced spelling, whose `];` lands on a later
		// physical line than the field name. Its lower bound is 0 rather
		// than 1 so the column still takes a zero DEFAULT: a `gte: 1`
		// would suppress the default (0 violates the CHECK), and a
		// read-only column that is NOT NULL, has no default and admits no
		// writer can never be populated — the CreateOrder path refuses to
		// generate at all, which is a different (correct) defect and would
		// stop this test before it ever reaches the marker assertions.
		"  int32 term_months = 4 [(buf.validate.field).int32 = {\n" +
		"    gte: 0\n" +
		"    lte: 12\n" +
		"  }]; // forge:read-only\n" +
		"  string note = 5;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append Order entity to orders proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	genProto := readFileE2E(t, protoPath)
	createReq := sliceBetweenCLI(genProto, "message CreateOrderRequest {", "}")
	if createReq == "" {
		t.Fatalf("CreateOrderRequest was not born into the proto:\n%s", genProto)
	}
	for _, readOnly := range []string{"unit_price_cents", "status", "term_months"} {
		if strings.Contains(createReq, readOnly) {
			t.Errorf("CreateOrderRequest kept the `// forge:read-only` field %q — the marker was dropped:\n%s",
				readOnly, createReq)
		}
	}
	for _, keep := range []string{"customer", "note"} {
		if !strings.Contains(createReq, keep) {
			t.Errorf("CreateOrderRequest dropped the client-settable field %q:\n%s", keep, createReq)
		}
	}

	// The entity message keeps every field — read-only trims the WRITE
	// side only; the column and the read surface are untouched.
	entityMsg := sliceBetweenCLI(genProto, "message Order {", "}")
	for _, readOnly := range []string{"unit_price_cents", "status", "term_months"} {
		if !strings.Contains(entityMsg, readOnly) {
			t.Errorf("read-only field %q must stay on the entity message:\n%s", readOnly, entityMsg)
		}
	}

	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}

// TestE2EScaffoldReadOnlyMarkerWithNoFieldFailsLoudly pins the other half of
// the contract: a marker forge CANNOT attach to a field is refused by name,
// never dropped. A silently-ignored marker ships a non-writable field to
// clients while its author believes it is protected — so birth writes
// nothing and the command exits non-zero.
func TestE2EScaffoldReadOnlyMarkerWithNoFieldFailsLoudly(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "ssxapp", "--mod", "example.com/ssxapp", "--service", "orders")
	projectDir := filepath.Join(dir, "ssxapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\n" +
		"message Order {\n" +
		"  string customer = 1;\n" +
		"  // forge:read-only\n" +
		"  message Line {\n" +
		"    string sku = 1;\n" +
		"  }\n" +
		"  string note = 2;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append Order entity to orders proto: %v", err)
	}

	cmd := exec.Command(forgeBin, "scaffold")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("`forge scaffold` exited 0 with a `// forge:read-only` marker that reached no field:\n%s", out)
	}
	text := string(out)
	for _, want := range []string{"forge:read-only", "orders.proto:", "attaches to no field"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal does not name %q:\n%s", want, text)
		}
	}

	// Refused before anything was written: no migration, no CRUD quintet.
	migEntries, _ := os.ReadDir(filepath.Join(projectDir, "db", "migrations"))
	for _, e := range migEntries {
		if strings.Contains(e.Name(), "_create_orders.") {
			t.Errorf("a refused birth still wrote %s", e.Name())
		}
	}
	if strings.Contains(readFileE2E(t, protoPath), "message CreateOrderRequest {") {
		t.Errorf("a refused birth still injected the CRUD quintet")
	}
}
