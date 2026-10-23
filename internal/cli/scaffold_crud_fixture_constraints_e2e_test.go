//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ECRUDFixtureSurvivesAddedConstraints pins the born CRUD lifecycle
// test against the constraints forge itself tells the author to add.
//
// handlers_crud_test.go is scaffold-once — "yours: scaffolded once, never
// touched again" — but the schema is not. It used to create two rows with
// IDENTICAL field values and no parent row for a `<x>_id` column. Both are
// fine until the constraints arrive:
//
//   - the FOREIGN KEY on `<x>_id`, which birth now WRITES LIVE (a resolved
//     reference is a real constraint from the first apply — see "scaffold:
//     birth migrations apply the foreign keys they resolve") → create #1
//     fails with failed_precondition, referenced record missing, unless the
//     born fixture seeds the parent;
//   - "add relationships, indexes, and constraints with hand-written
//     migrations", forge's own advice for everything birth cannot derive →
//     the first UNIQUE index fails create #2 with already_exists.
//
// Identical rows bought nothing (the test needs two DISTINCT records to
// prove list/get/update), so the fixtures now differ everywhere the type
// admits a second value, and the parent is seeded because the FK is real.
func TestE2ECRUDFixtureSurvivesAddedConstraints(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "cfcapp", "--mod", "example.com/cfcapp", "--service", "widget")
	projectDir := filepath.Join(dir, "cfcapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\n" +
		"message Brand {\n" +
		"  string name = 1;\n" +
		"}\n" +
		"\n// forge:entity\n" +
		"message Gadget {\n" +
		"  string name = 1;\n" +
		"  string sku = 2;\n" +
		"  int64 seq = 3;\n" +
		"  string brand_id = 4;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append entities to widget proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// Birth writes the resolved FOREIGN KEY LIVE — not commented out. This
	// is what makes the parent-row seeding below load-bearing rather than
	// decorative: without the constraint, create #1 would pass on a
	// dangling brand_id and the fixture would prove nothing.
	gadgetsUp := readBornMigrationE2E(t, projectDir, "gadgets")
	if !strings.Contains(gadgetsUp, "REFERENCES brands") {
		t.Errorf("gadgets birth migration must apply the resolved brand_id FOREIGN KEY, not suggest it:\n%s", gadgetsUp)
	}
	for _, line := range strings.Split(gadgetsUp, "\n") {
		if strings.Contains(line, "REFERENCES") && strings.HasPrefix(strings.TrimSpace(line), "--") {
			t.Errorf("birth must not emit a COMMENTED-OUT foreign key (dangling-by-default): %q", strings.TrimSpace(line))
		}
	}

	// The born test carries no fixtures: it builds its rows from the
	// regenerated create-request factory, which seeds the parent and gives
	// the two variants distinct values everywhere the type admits one.
	crudTestPath := filepath.Join(projectDir, "internal", "handlers", "widget", "handlers_crud_test.go")
	crudTest := readFileE2E(t, crudTestPath)
	if !strings.Contains(crudTest, "widget.NewCreateGadgetRequest(t, db, 1)") || strings.Contains(crudTest, "INSERT INTO") {
		t.Errorf("the born test must build its rows from the regenerated factory, not literal fixtures:\n%s", crudTest)
	}
	factories := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "widget", "factories_gen_test.go"))
	if strings.Count(factories, `"test-value"`) != strings.Count(factories, `"test-value-2"`) {
		t.Errorf("variant 0 and variant 1 do not carry matching distinct literals:\n%s", factories)
	}
	if !strings.Contains(factories, `INSERT INTO "brands"`) {
		t.Errorf("the create-request factory seeds no parent row for brand_id, the FK birth applies:\n%s", factories)
	}
	runCmd(t, projectDir, "go", "test", "-count=1", "./internal/handlers/widget/")

	// Now take forge's own advice, by hand, after birth: UNIQUE indexes on
	// three differently-typed columns. (The FOREIGN KEY is NOT here — birth
	// already applied it, asserted above. Re-adding it by hand would fail
	// with "constraint already exists", which is the schema saying the same
	// thing.)
	up := "CREATE UNIQUE INDEX gadgets_sku_uniq ON gadgets (sku);\n" +
		"CREATE UNIQUE INDEX gadgets_seq_uniq ON gadgets (seq);\n" +
		"CREATE UNIQUE INDEX gadgets_name_uniq ON gadgets (name);\n"
	if err := os.WriteFile(nextMigrationPathE2E(t, projectDir, "constraints"), []byte(up), 0o644); err != nil {
		t.Fatalf("write constraints migration: %v", err)
	}

	// The scaffold-once test must survive UNCHANGED — that is the contract —
	// and pass after the regenerate that refreshes its factories.
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "test", "-count=1", "./internal/handlers/widget/")
	if readFileE2E(t, crudTestPath) != crudTest {
		t.Errorf("handlers_crud_test.go changed; it is scaffold-once and must survive as written")
	}
}

// TestE2ECRUDLifecycleSurvivesBirthMigrationEdits is the reproduction for the
// dogfood finding this shape exists for. The db skill tells authors to edit
// the BIRTH migration right after scaffolding — make a derived column
// GENERATED, add one-way lifecycle CHECKs — and the born lifecycle test used
// to break on both, because it froze literal fixtures:
//
//	seed parent rows: pq: cannot insert a non-DEFAULT value into column "remaining_cents" (428C9)
//	create #2: invalid_argument: ... violates check constraint "tickets_resolved_has_stamp"
//
// Now the test builds its rows from the regenerated factories, so a
// `forge generate` after the edit is all it takes — the owned file is not
// touched.
func TestE2ECRUDLifecycleSurvivesBirthMigrationEdits(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "edits", "--mod", "example.com/edits", "--service", "ops")
	projectDir := filepath.Join(dir, "edits")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "ops", "v1", "ops.proto")
	proto := strings.Replace(readFileE2E(t, protoPath),
		`import "forge/v1/forge.proto";`,
		"import \"forge/v1/forge.proto\";\nimport \"google/protobuf/timestamp.proto\";", 1)
	proto += "\nenum TicketStatus {\n" +
		"  TICKET_STATUS_UNSPECIFIED = 0;\n" +
		"  TICKET_STATUS_OPEN = 1;\n" +
		"  TICKET_STATUS_RESOLVED = 2;\n" +
		"}\n" +
		"\n// forge:entity\n" +
		"message Project {\n" +
		"  string name = 1;\n" +
		"  int64 budget_cents = 2;\n" +
		"  int64 spent_cents = 3;\n" +
		"  int64 remaining_cents = 4;\n" +
		"}\n" +
		"\n// forge:entity\n" +
		"message Ticket {\n" +
		"  string project_id = 1;\n" +
		"  string title = 2;\n" +
		"  TicketStatus status = 3;\n" +
		"  google.protobuf.Timestamp resolved_at = 4;\n" +
		"  int64 hours = 5;\n" +
		"  int64 rate_cents = 6;\n" +
		"  int64 total_cents = 7;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append entities: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	crudTestPath := filepath.Join(projectDir, "internal", "handlers", "ops", "handlers_crud_test.go")
	crudTest := readFileE2E(t, crudTestPath)

	// Edit the BIRTH migrations, exactly as the db skill recommends.
	editBorn := func(table, from, to, appendSQL string) {
		t.Helper()
		path := bornMigrationPathE2E(t, projectDir, table)
		body := readFileE2E(t, path)
		if !strings.Contains(body, from) {
			t.Fatalf("birth migration for %s has no %q to edit:\n%s", table, from, body)
		}
		body = strings.Replace(body, from, to, 1) + appendSQL
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("edit %s: %v", path, err)
		}
	}
	editBorn("projects",
		"remaining_cents BIGINT NOT NULL DEFAULT 0",
		"remaining_cents BIGINT GENERATED ALWAYS AS (budget_cents - spent_cents) STORED", "")
	editBorn("tickets",
		"total_cents BIGINT NOT NULL DEFAULT 0",
		"total_cents BIGINT GENERATED ALWAYS AS (hours * rate_cents) STORED",
		"\nALTER TABLE tickets ADD CONSTRAINT tickets_resolved_has_stamp\n"+
			"    CHECK (status <> 'TICKET_STATUS_RESOLVED' OR resolved_at IS NOT NULL);\n")

	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "test", "-count=1", "./internal/handlers/ops/")
	if readFileE2E(t, crudTestPath) != crudTest {
		t.Errorf("handlers_crud_test.go changed; it is scaffold-once and must survive as written")
	}

	// The factory a test author reaches for inserts a MINIMAL ticket: status
	// at its DEFAULT, no resolution stamp, the GENERATED column left alone.
	factories := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "ops", "factories_gen_test.go"))
	for _, frozen := range []string{`"remaining_cents"`, `"total_cents"`, `"resolved_at"`, "TotalCents:", "ResolvedAt:", "Status:"} {
		if strings.Contains(factories, frozen) {
			t.Errorf("regenerated factories still write %s:\n%s", frozen, factories)
		}
	}
}
