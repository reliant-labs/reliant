package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func writeColumnMarkersFixture(t *testing.T, name, sql string) string {
	t.Helper()
	dir := t.TempDir()
	migDir := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, name), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	return migDir
}

func TestCollectColumnMarkerFindings_FlagsUnknownMarker(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_immutable.up.sql", `
COMMENT ON COLUMN invoices.total_cents IS 'forge:immutible';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Marker != "forge:immutible" {
		t.Errorf("marker = %q, want forge:immutible", f.Marker)
	}
	if f.Object != "invoices.total_cents" {
		t.Errorf("object = %q, want invoices.total_cents", f.Object)
	}
	if f.Line != 2 {
		t.Errorf("line = %d, want 2", f.Line)
	}
}

func TestCollectColumnMarkerFindings_KnownMarkersAreClean(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_known.up.sql", `
COMMENT ON COLUMN invoices.total_cents IS 'forge:immutable';
COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:ref derived-from=prescription_id';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
}

func TestCollectColumnMarkerFindings_IgnoresProseWithoutForgeToken(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_prose.up.sql", `
COMMENT ON COLUMN invoices.notes IS 'free-text notes field, nothing special';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings for a plain comment, got %+v", findings)
	}
}

func TestCollectColumnMarkerFindings_RejectsPrefixCollision(t *testing.T) {
	// forge:refactor must NOT be silently accepted as forge:ref — exact
	// token match only.
	dir := writeColumnMarkersFixture(t, "0001_prefix.up.sql", `
COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:refactor later';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for forge:refactor, got %d: %+v", len(findings), findings)
	}
	if findings[0].Marker != "forge:refactor" {
		t.Errorf("marker = %q, want forge:refactor", findings[0].Marker)
	}
}

// A marker whose argument attaches with `=` arrives as ONE token
// (forge:fill=handler), so the registry comparison has to look at the part
// before the `=` too. forge:ref takes its argument after a space and so never
// exercised this; the gap appeared the moment a `=`-style marker existed, and
// showed up as a real project being warned about a marker forge had just
// shipped. Both spellings are pinned: alone, and alongside another marker.
func TestCollectColumnMarkerFindings_ValuedMarkerIsRecognized(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_valued.up.sql", `
COMMENT ON COLUMN invoices.number IS 'forge:fill=handler';
COMMENT ON COLUMN invoices.company_id IS 'forge:immutable forge:fill=handler';
COMMENT ON COLUMN invoices.token IS 'forge:fill=ulid';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("forge:fill=<strategy> is a known marker, got findings: %+v", findings)
	}
}

// Splitting on `=` must not weaken the exact-match guarantee: a longer marker
// that merely STARTS with a known one is still unknown, however it is spelled.
func TestCollectColumnMarkerFindings_ValuedPrefixCollisionStillRejected(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_valued_prefix.up.sql", `
COMMENT ON COLUMN invoices.number IS 'forge:fillmode=handler';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for forge:fillmode, got %d: %+v", len(findings), findings)
	}
	if findings[0].Marker != "forge:fillmode" {
		t.Errorf("marker = %q, want forge:fillmode", findings[0].Marker)
	}
}

func TestCollectColumnMarkerFindings_MissingDirIsNotAnError(t *testing.T) {
	findings, err := collectColumnMarkerFindings(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
}

func TestColumnMarkerFixHint_ListsKnownMarkers(t *testing.T) {
	findings := findUnknownColumnMarkers("x.up.sql", `COMMENT ON COLUMN invoices.total_cents IS 'forge:immutible';`)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}
	hint := columnMarkerFixHint(findings[0])
	for _, want := range []string{"forge:immutible", "invoices.total_cents", "forge:immutable", "forge:no-soft-delete", "forge project annotations --kind column"} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

// The lane reads every catalog comment forge declares markers in, with each
// object's own vocabulary: a table marker on a column, a column marker on a
// table, an unknown table marker and forge:no-soft-delete off deleted_at all
// do nothing, and each is a finding. The same comments are clean where they
// belong.
func TestCollectColumnMarkerFindings_EveryObjectHasItsOwnVocabulary(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_sites.up.sql", `
COMMENT ON TABLE payments IS 'forge:append-only — a ledger';
COMMENT ON VIEW chats_with_activity IS 'forge:entity key=id';
COMMENT ON COLUMN worktrees.deleted_at IS 'forge:no-soft-delete — the archive stamp';
COMMENT ON TABLE invoices IS 'forge:appendonly';
COMMENT ON TABLE orders IS 'forge:immutable';
COMMENT ON COLUMN orders.total IS 'forge:no-entity';
COMMENT ON COLUMN worktrees.archived_at IS 'forge:no-soft-delete';
COMMENT ON MATERIALIZED VIEW stats IS 'forge:append-only';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	want := map[int]string{
		5: "did you mean forge:append-only?",
		6: "is a column marker",
		7: "is a table marker",
		8: "only applies to the deleted_at column",
		9: "is a table marker",
	}
	if len(findings) != len(want) {
		t.Fatalf("expected %d findings, got %d: %+v", len(want), len(findings), findings)
	}
	for _, f := range findings {
		if !strings.Contains(f.Problem, want[f.Line]) {
			t.Errorf("line %d: problem %q, want it to contain %q", f.Line, f.Problem, want[f.Line])
		}
	}
}

func TestCollectColumnMarkersJSON_Shape(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_immutable.up.sql", `
COMMENT ON COLUMN invoices.total_cents IS 'forge:immutible';
`)
	cfg := &config.ProjectConfig{Database: config.DatabaseConfig{MigrationsDir: dir}}
	fs, err := collectColumnMarkersJSON(cfg)
	if err != nil {
		t.Fatalf("collectColumnMarkersJSON: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 JSON finding, got %d: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Rule != "forge-column-markers" {
		t.Errorf("rule = %q, want forge-column-markers", f.Rule)
	}
	if f.Severity != lintSevError {
		t.Errorf("severity = %q, want %q (the lane gates)", f.Severity, lintSevError)
	}
	if f.FixHint == "" {
		t.Error("missing fix_hint")
	}
}

// A COMMENT ON COLUMN whose object is schema-qualified and quoted
// (`"public"."invoices".total_cents`) is still read: the pattern used to stop
// at the first quote and miss the statement, so a misspelled marker there
// went unreported.
func TestCollectColumnMarkerFindings_QualifiedQuotedObject(t *testing.T) {
	dir := writeColumnMarkersFixture(t, "0001_qualified.up.sql", `
COMMENT ON COLUMN "public"."invoices".total_cents IS 'forge:immutible';
COMMENT ON CONSTRAINT invoices_customer_id_fkey ON public."invoices" IS 'forge:reff';
`)
	findings, err := collectColumnMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectColumnMarkerFindings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (column and constraint), got %d: %+v", len(findings), findings)
	}
}

// The read-only-fields schema replay resolves a schema-qualified, quoted
// table in CREATE, ALTER and COMMENT ON COLUMN to one table — the pg_dump
// shape — rather than to a table named after the schema.
func TestApplyMigrationColumns_SchemaQualified(t *testing.T) {
	live := map[string]map[string]sqlColumn{}
	sql := `CREATE TABLE public.invoices (id text PRIMARY KEY);
ALTER TABLE ONLY "public"."invoices" ADD COLUMN total_cents bigint NOT NULL DEFAULT 0;
COMMENT ON COLUMN public."invoices".total_cents IS 'forge:fill=handler';
`
	applyMigrationColumns(live, sql)
	applyFillMarkers(live, sql)
	if _, ok := live["public"]; ok {
		t.Fatalf("a schema was read as a table: %v", live)
	}
	col, ok := live["invoices"]["total_cents"]
	if !ok {
		t.Fatalf("invoices.total_cents not replayed: %v", live)
	}
	if !col.FillDeclared {
		t.Errorf("forge:fill on a qualified COMMENT ON COLUMN was not applied: %+v", col)
	}
}
