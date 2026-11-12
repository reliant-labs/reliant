// File: internal/cli/lint/lint_read_only_fields_test.go
//
// Tests for forgeconv-read-only-field-unwritten. Each case builds a small
// project on disk — proto tree, migrations, and Go source — because the
// check's whole job is to correlate all three, and a test that stubbed any
// side would not exercise the correlation.
//
// The false-positive cases outnumber the firing case on purpose. A lint
// that fires on a GENERATED column or on created_at gets switched off, and
// a switched-off rule protects nothing — so every exclusion is pinned here
// rather than left to the reader of the implementation.

package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readOnlyProject writes a project with one proto, one migration, and an
// optional handler file, returning the project root.
func readOnlyProject(t *testing.T, protoBody, migrationSQL, handlerGo string) string {
	t.Helper()
	root := t.TempDir()
	protoDir := filepath.Join(root, "proto", "services", "estimates", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "estimates.proto"), []byte(protoBody), 0o644); err != nil {
		t.Fatal(err)
	}
	migDir := filepath.Join(root, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "20260101000000_init.up.sql"), []byte(migrationSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	if handlerGo != "" {
		handlerDir := filepath.Join(root, "internal", "handlers", "estimates")
		if err := os.MkdirAll(handlerDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(handlerDir, "handlers_crud.go"), []byte(handlerGo), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// estimateProto is the motivating shape: a money column the author took off
// the write surface with forge:read-only and never derived.
const estimateProto = `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  string title = 2;

  // Totals in cents, maintained by RecalculateEstimate.
  int64 total_cents = 3; // forge:read-only

  string created_at = 4;
  string updated_at = 5;
}
`

// estimateMigration is the schema the audited project shipped: a plain
// BIGINT with a zero DEFAULT, which is what makes the failure silent.
const estimateMigration = `CREATE TABLE estimates (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL,
    total_cents  BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// TestReadOnlyFields_TripsWhenNothingWrites is the defect this rule exists
// for, and the highest-severity silent failure the audit found: the field
// is off every write envelope, nothing derives it, the column default is 0,
// and every total renders $0.00 with no error and no failing test.
func TestReadOnlyFields_TripsWhenNothingWrites(t *testing.T) {
	root := readOnlyProject(t, estimateProto, estimateMigration, `package estimates

// A handler that delegates and derives nothing.
func (s *Service) CreateEstimate() error {
	return nil
}
`)
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Field != "total_cents" || f.GoField != "TotalCents" || f.Entity != "Estimate" {
		t.Errorf("wrong field flagged: %+v", f)
	}
	if f.Table != "estimates" {
		t.Errorf("want table estimates, got %q", f.Table)
	}
	// The finding points at the proto FIELD declaration (line 11), not at
	// the doc comment above it (line 10) — the marker is on the field, and
	// that is the line the author edits.
	if f.Line != 11 {
		t.Errorf("want the proto declaration line 11, got %d", f.Line)
	}
	hint := readOnlyFieldFixHint(f)
	for _, want := range []string{"TotalCents", "GENERATED ALWAYS AS", "$0.00"} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q: %s", want, hint)
		}
	}
}

// TestReadOnlyFields_TimestampIsNotDescribedAsMoney is the dogfood report
// this consequence clause was rewritten for. The clause used to splice a
// fixed money illustration into every finding, so a TIMESTAMPTZ column was
// told it "ships as $0.00" — technically an aside about the bug class, and
// read by a careful human as forge having mis-detected the column as
// numeric. They went looking for a proto type error that did not exist.
//
// The consequence must describe THIS column: a nullable timestamp stays
// NULL forever, which is both true and just as alarming.
func TestReadOnlyFields_TimestampIsNotDescribedAsMoney(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  string quarantined_at = 2; // forge:read-only
}
`, `CREATE TABLE estimates (
    id             TEXT PRIMARY KEY,
    quarantined_at TIMESTAMPTZ
);
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(findings), findings)
	}
	hint := readOnlyFieldFixHint(findings[0])
	for _, unwanted := range []string{"$0.00", "money"} {
		if strings.Contains(hint, unwanted) {
			t.Errorf("a TIMESTAMPTZ column must not be described with %q:\n%s", unwanted, hint)
		}
	}
	if !strings.Contains(hint, "NULL") {
		t.Errorf("the consequence for a nullable timestamp is that it stays NULL forever:\n%s", hint)
	}
}

// TestReadOnlyFields_MoneyKeepsTheVividConsequence is the other half. The
// money framing is why this rule caught real defects in two separate
// dogfood runs, so making the clause type-specific must not flatten the
// case it was written for.
func TestReadOnlyFields_MoneyKeepsTheVividConsequence(t *testing.T) {
	root := readOnlyProject(t, estimateProto, estimateMigration, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(findings), findings)
	}
	hint := readOnlyFieldFixHint(findings[0])
	if !strings.Contains(hint, "$0.00") {
		t.Errorf("a *_cents column must still get the vivid money consequence:\n%s", hint)
	}
}

// TestReadOnlyFields_SilentWhenHandlerWrites is the primary false-positive
// guard: a derivation exists, so the rule must say nothing.
func TestReadOnlyFields_SilentWhenHandlerWrites(t *testing.T) {
	root := readOnlyProject(t, estimateProto, estimateMigration, `package estimates

func (s *Service) RecalculateEstimate(row *Estimate, lines []*LineItem) {
	var sum int64
	for _, l := range lines {
		sum += l.AmountCents
	}
	row.TotalCents = sum
}
`)
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("rule fired on a field that IS written — the write is right there:\n%+v", findings)
	}
}

// TestReadOnlyFields_GeneratedColumnIsTheFix is the exclusion that matters
// most, because the rule's own fix hint recommends it. A GENERATED ALWAYS
// AS (...) STORED column is written by postgres on every insert and update;
// firing on it would tell the author to fix code that is already correct,
// in the exact words that told them to write it.
func TestReadOnlyFields_GeneratedColumnIsTheFix(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message EstimateLineItem {
  string id = 1;
  int64 quantity_milli = 2;
  int64 unit_price_cents = 3;
  int64 amount_cents = 4; // forge:read-only
}
`, `CREATE TABLE estimate_line_items (
    id               TEXT PRIMARY KEY,
    quantity_milli   BIGINT NOT NULL DEFAULT 0,
    unit_price_cents BIGINT NOT NULL DEFAULT 0,
    amount_cents     BIGINT NOT NULL GENERATED ALWAYS AS (quantity_milli * unit_price_cents / 1000) STORED
);
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a GENERATED column is written by postgres — that IS the fix this rule recommends:\n%+v", findings)
	}
}

// TestReadOnlyFields_ManagedTimestampsAreNotUnwritten pins the second
// load-bearing exclusion. created_at/updated_at are read-only by nature and
// stamped by pkg/crud, which is generated — so the Go scan cannot see the
// write and a naive rule flags every entity in the project at once. That is
// the shape that gets a lint globally disabled on day one.
func TestReadOnlyFields_ManagedTimestampsAreNotUnwritten(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  string title = 2;
  // forge:read-only
  string created_at = 3;
  // forge:read-only
  string updated_at = 4;
  // forge:read-only
  string deleted_at = 5;
}
`, `CREATE TABLE estimates (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("forge's own managed-timestamp machinery writes these:\n%+v", findings)
	}
}

// TestReadOnlyFields_MeaningfulDefaultIsIntent pins that a non-zero column
// DEFAULT is evidence somebody chose the value. A status column defaulting
// to 'draft' is fully populated by the insert; there is nothing silent
// about it and nothing to report.
func TestReadOnlyFields_MeaningfulDefaultIsIntent(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  string status = 2; // forge:read-only
  int64 retry_limit = 3; // forge:read-only
}
`, `CREATE TABLE estimates (
    id          TEXT PRIMARY KEY,
    status      TEXT NOT NULL DEFAULT 'draft',
    retry_limit BIGINT NOT NULL DEFAULT 5
);
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a non-zero DEFAULT is intent — the column is populated:\n%+v", findings)
	}
}

// TestReadOnlyFields_NotNullNoDefaultBelongsToGenerate pins the boundary
// with FindUnsatisfiableColumns, which already FAILS `forge generate` for
// exactly this shape (NOT NULL, no DEFAULT, read-only, no forge:fill).
// Reporting it here too would put a warning next to a hard error for one
// defect, and the warning would read like the softer of two verdicts.
func TestReadOnlyFields_NotNullNoDefaultBelongsToGenerate(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  string company_id = 2; // forge:read-only
}
`, `CREATE TABLE estimates (
    id         TEXT PRIMARY KEY,
    company_id TEXT NOT NULL
);
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("NOT NULL + no DEFAULT is generate's gating error, not this warning:\n%+v", findings)
	}
}

// TestReadOnlyFields_FillMarkerIsAnAnswer pins that `forge:fill=` already
// declares who populates the column — forge itself for ulid, the handler
// author for handler. The question this rule asks has been answered.
func TestReadOnlyFields_FillMarkerIsAnAnswer(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  int64 total_cents = 2; // forge:read-only
}
`, `CREATE TABLE estimates (
    id          TEXT PRIMARY KEY,
    total_cents BIGINT NOT NULL DEFAULT 0
);

COMMENT ON COLUMN estimates.total_cents IS 'forge:fill=handler';
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("forge:fill= declares who populates the column:\n%+v", findings)
	}
}

// TestReadOnlyFields_ComputedIsTheOtherRulesJob pins that the two markers
// do not both report the same field. forge:computed already has
// forgeconv-computed-field-unwritten; reporting it twice, under two rule
// ids with two different fix hints, teaches authors to ignore both.
func TestReadOnlyFields_ComputedIsTheOtherRulesJob(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  int64 total_cents = 2; // forge:computed
}
`, `CREATE TABLE estimates (
    id          TEXT PRIMARY KEY,
    total_cents BIGINT NOT NULL DEFAULT 0
);
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("forge:computed belongs to forgeconv-computed-field-unwritten:\n%+v", findings)
	}
}

// TestReadOnlyFields_UnknownColumnIsSilent pins the rule's failure
// direction. The schema side is a TEXT parse of migrations, so a column it
// cannot resolve — a table named by a convention the mapper does not
// derive, a CREATE TABLE this parser does not understand — must produce
// silence, never a finding. A rule whose failure mode is a false positive
// is the one that gets disabled.
func TestReadOnlyFields_UnknownColumnIsSilent(t *testing.T) {
	root := readOnlyProject(t, estimateProto, `-- a schema this parser will not resolve the entity against
CREATE TABLE legacy_estimate_archive (
    id TEXT PRIMARY KEY
);
`, "package estimates\n")
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("an unresolved column must be silent, not a finding:\n%+v", findings)
	}
}

// TestReadOnlyFields_GeneratedAndTestWritesDoNotCount matches the computed
// rule's exclusion exactly, for the same reason: the generated conversion
// assigns EVERY field, so counting generated writes would satisfy every
// read-only field and the check would never fire at all.
func TestReadOnlyFields_GeneratedAndTestWritesDoNotCount(t *testing.T) {
	root := readOnlyProject(t, estimateProto, estimateMigration, "")
	handlerDir := filepath.Join(root, "internal", "handlers", "estimates")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"), []byte(`package estimates

func toProto(e *Estimate, m *pb.Estimate) {
	m.TotalCents = e.TotalCents
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "factories_test.go"), []byte(`package estimates

func factory() *Estimate {
	row := &Estimate{}
	row.TotalCents = 1234
	return row
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("generated/test writes must not satisfy the obligation, got %d findings: %+v",
			len(findings), findings)
	}
}

// TestReadOnlyFields_NoProtoTreeIsClean pins that CLI/library projects,
// which have no proto tree, lint clean rather than erroring.
func TestReadOnlyFields_NoProtoTreeIsClean(t *testing.T) {
	findings, err := collectReadOnlyFieldFindings(t.TempDir(), filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("a project with no proto tree must not error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want no findings, got %+v", findings)
	}
}
