//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EScaffoldReadOnlyColumnTakesItsSchemaDefault pins the create path
// for a column the client cannot set.
//
// A `// forge:read-only` field is absent from the born create request, so
// the generated op left the Go field at its zero value and Bun wrote that
// zero. For an enum column — born as
//
//	tone TEXT NOT NULL DEFAULT 'TONE_WARM' CHECK (tone IN (...))
//
// — the zero is the empty string, which violates the CHECK forge derived from the same
// proto. Every create failed, out of the box, on a marker forge documents:
//
//	--- FAIL: TestCRUD_Gadget_Lifecycle
//	    create #1: invalid_argument: create gadget: a field value violates a constraint
//
// A field the request does not carry is now left UNSET, and the database
// applies the column's own DEFAULT (F12.5, #990): the born enum column is
// ,nullzero (its CHECK rejects the zero, so Create omits a zero), and a scalar
// whose DEFAULT is not its Go zero is a pointer whose nil is unset. The
// second half of the test is why that distinction lives in the entity type
// and not in a `default:` bun tag: Bun reads every zero as unset, so a tag
// would silently store true for an explicit false on a hand-written
// `DEFAULT true` column. A *bool can tell.
func TestE2EScaffoldReadOnlyColumnTakesItsSchemaDefault(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "ssdapp", "--mod", "example.com/ssdapp", "--service", "widget")
	projectDir := filepath.Join(dir, "ssdapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\nenum Tone {\n" +
		"  TONE_UNSPECIFIED = 0;\n" +
		"  TONE_WARM = 1;\n" +
		"  TONE_COOL = 2;\n" +
		"}\n" +
		"\n// forge:entity\n" +
		"message Gadget {\n" +
		"  string name = 1;\n" +
		"  Tone tone = 2; // forge:read-only\n" +
		"  int64 priority = 3; // forge:read-only\n" +
		"  bool active = 4;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("append Gadget entity to widget proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// The born CRUD lifecycle test runs the real stack against the real
	// migrations. This is the reproduction, verbatim.
	runCmd(t, projectDir, "go", "test", "-count=1", "./internal/handlers/widget/")

	opsPath := filepath.Join(projectDir, "internal", "handlers", "widget", "handlers_crud_ops_gen.go")
	createOp := sliceBetweenCLI(readFileE2E(t, opsPath), "func (s *Service) crudCreateGadgetOp()", "Persist:")
	// The op pastes no DEFAULT: the database applies it. The born migration
	// defaults an enum column to the first REAL member (TONE_WARM), so what a
	// read-only field gets is a state the domain actually has — and the
	// born lifecycle test above proves the create succeeds against the CHECK.
	if strings.Contains(createOp, "e.Tone =") {
		t.Errorf("the create op must leave a read-only enum to the column DEFAULT:\n%s", createOp)
	}
	if strings.Contains(createOp, "UNSPECIFIED") {
		t.Errorf("a read-only enum must not be born at the proto zero:\n%s", createOp)
	}

	// Evolution the charter tells authors to write by hand: a non-zero
	// default plus a range CHECK on the read-only column, and a
	// `DEFAULT true` on a column the client DOES set.
	up := "ALTER TABLE gadgets ALTER COLUMN priority SET DEFAULT 5;\n" +
		"ALTER TABLE gadgets ADD CONSTRAINT gadgets_priority_range CHECK (priority BETWEEN 1 AND 10);\n" +
		"ALTER TABLE gadgets ALTER COLUMN active SET DEFAULT true;\n"
	if err := os.WriteFile(nextMigrationPathE2E(t, projectDir, "tighten_gadgets"), []byte(up), 0o644); err != nil {
		t.Fatalf("write evolution migration: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	createOp = sliceBetweenCLI(readFileE2E(t, opsPath), "func (s *Service) crudCreateGadgetOp()", "Persist:")
	if strings.Contains(createOp, "e.Priority =") {
		t.Errorf("the create op must leave a read-only column to its hand-written DEFAULT 5:\n%s", createOp)
	}
	// `active` IS on the create request: its value comes from the caller and
	// nothing may substitute the column default for an explicit false.
	if !strings.Contains(createOp, "v := req.Active") || !strings.Contains(createOp, "e.Active = &v") {
		t.Errorf("a client-settable column must come from the request, explicit false included:\n%s", createOp)
	}
	if strings.Contains(createOp, "e.Active = true") {
		t.Errorf("the column DEFAULT was written over a client-settable field — an explicit false would be lost:\n%s", createOp)
	}
	if orm := readFileE2E(t, filepath.Join(projectDir, "internal", "db", "gadget_orm_gen.go")); !regexp.MustCompile(`\bActive\s+\*bool\b`).MatchString(orm) {
		t.Errorf("a DEFAULT true column must be a *bool, so an explicit false is stored rather than read as unset:\n%s", orm)
	}

	runCmd(t, projectDir, "go", "test", "-count=1", "./internal/handlers/widget/")
	runCmd(t, projectDir, "go", "vet", "./...")
}
