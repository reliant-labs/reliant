// File: internal/cli/lint/lint_read_only_fields_gating_test.go
//
// Probes for the two ways forgeconv-read-only-field-unwritten could fire on
// a legitimate schema. They decide whether the rule may GATE the build:
// a gating rule that fires wrongly is far worse than a warning that does,
// because the author's only recourse is to switch it off, and a rule that
// is off protects nothing.
//
// Each case here is a schema a competent author would write. If the rule
// fires on one, it is not ready to gate.

package lint

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadOnlyFields_Gates is the verdict change. The rule was warnings-only,
// which is very close to no signal at all for the one failure mode that has
// no other symptom: the argument for the check is that this bug "ships as
// $0.00 with no error, no failing test, and no log line anywhere", and a
// warning inside a hundred-line lint run is a log line nobody reads.
//
// Gating is only defensible because the rule's exclusions hold — every one
// is pinned in lint_read_only_fields_test.go, and the two write paths that
// were invisible to it (a trigger, a column-named UPDATE) are pinned above.
func TestReadOnlyFields_Gates(t *testing.T) {
	step := lintStepNamed(t, "read-only-fields lint")
	if !step.gates {
		t.Fatalf("read-only-fields must GATE: a warning for a defect whose only symptom is a " +
			"human reading $0.00 on a screen is indistinguishable from no report at all")
	}

	root := readOnlyProject(t, estimateProto, estimateMigration, `package estimates

// A handler that delegates and derives nothing.
func noop() {}
`)
	findings, err := collectReadOnlyFieldsJSON(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("no findings — this case is the rule's motivating defect")
	}
	for _, f := range findings {
		if f.Severity != lintSevError {
			t.Errorf("finding %s is severity %q; a gating step's findings must be %q or the JSON "+
				"report's ok stays true while the text run fails", f.Rule, f.Severity, lintSevError)
		}
	}
}

// TestReadOnlyFields_CleanProjectDoesNotGate is the other half: now that the
// rule can fail a build, a project it has nothing to say about must not.
func TestReadOnlyFields_CleanProjectDoesNotGate(t *testing.T) {
	root := readOnlyProject(t, estimateProto, `CREATE TABLE estimates (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL,
    total_cents  BIGINT NOT NULL GENERATED ALWAYS AS (0) STORED,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
`, "")
	findings, err := collectReadOnlyFieldsJSON(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a GENERATED column is the rule's own recommended fix; gating on it would fail "+
			"the build on the correct schema. Got %+v", findings)
	}
}

// lintStepNamed returns the pipeline step with the given name.
func lintStepNamed(t *testing.T, name string) linterStep {
	t.Helper()
	for _, s := range lintPipeline() {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("no lint step named %q", name)
	return linterStep{}
}

// A nullable column populated by a SQL-level update — the ORM path that
// names the COLUMN, not the Go struct field. The Go scan looks for an
// assignment to the protoc-gen-go field name, so a write spelled as a
// column string is invisible to it.
//
// This is not exotic: a partial update through a column set is the ordinary
// way to touch one column without reading the row first.
func TestReadOnlyFields_ColumnStringWriteIsInvisibleToTheGoScan(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  string title = 2;

  // Set when a reviewer approves; NULL until then.
  string approved_at = 3; // forge:read-only

  string created_at = 4;
  string updated_at = 5;
}
`, `CREATE TABLE estimates (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL,
    approved_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
`, `package estimates

// Approve stamps the column through the ORM's column-name API, which never
// mentions the Go field identifier the lint's AST scan searches for.
func Approve(ctx context.Context, db *bun.DB, id string) error {
	_, err := db.NewUpdate().Model((*Estimate)(nil)).
		Set("approved_at = now()").
		Where("id = ?", id).Exec(ctx)
	return err
}
`)

	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a column written through its COLUMN NAME must not be reported as unwritten; "+
			"the AST scan only sees the Go field identifier, so a partial update spelled as SQL "+
			"is invisible to it. As a GATING rule this fails the build on correct code. Got %+v",
			findings)
	}
}

// A column populated by a database TRIGGER declared in the migrations
// themselves. The write is real and it is in the schema the rule already
// reads — so reporting it as unpopulated is reporting against the evidence
// in hand.
func TestReadOnlyFields_TriggerInMigrationsIsAWrite(t *testing.T) {
	root := readOnlyProject(t, estimateProto, estimateMigration+`
CREATE FUNCTION recalc_estimate_total() RETURNS TRIGGER AS $$
BEGIN
    NEW.total_cents := (SELECT COALESCE(SUM(amount_cents), 0)
                        FROM estimate_line_items WHERE estimate_id = NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER estimates_recalc BEFORE INSERT OR UPDATE ON estimates
    FOR EACH ROW EXECUTE FUNCTION recalc_estimate_total();
`, "")

	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a column assigned by a trigger function IN THE MIGRATIONS is populated, and the "+
			"rule already reads those files — reporting it as unwritten contradicts its own "+
			"evidence. As a GATING rule this fails the build on correct code. Got %+v", findings)
	}
}

// A DEFAULT the migration text parser may not resolve. If a real default is
// lost in the parse, the column reads as "nullable with no default" and the
// rule fires — on a schema that populates the column perfectly well.
func TestReadOnlyFields_DefaultFormsTheParserMustResolve(t *testing.T) {
	cases := []struct {
		name string
		ddl  string
	}{
		{"cast literal", `status TEXT DEFAULT 'draft'::text`},
		{"expression", `expires_at TIMESTAMPTZ DEFAULT (now() + interval '30 days')`},
		{"function", `token TEXT DEFAULT gen_random_uuid()::text`},
		{"multiline", "note        TEXT\n        DEFAULT 'none'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			col := firstWord(c.ddl)
			root := readOnlyProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  string `+col+` = 2; // forge:read-only
  string created_at = 3;
  string updated_at = 4;
}
`, `CREATE TABLE estimates (
    id           TEXT PRIMARY KEY,
    `+c.ddl+`,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
`, "")

			findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if len(findings) > 0 {
				t.Errorf("a real DEFAULT (%s) must take the column out of scope, but the rule "+
					"fired %d time(s): %+v — as a GATING rule this would fail the build on a "+
					"schema that populates the column", c.ddl, len(findings), findings)
			}
		})
	}
}

// firstWord is the column name at the head of a DDL fragment.
func firstWord(ddl string) string {
	for i, r := range ddl {
		if r == ' ' || r == '\t' || r == '\n' {
			return ddl[:i]
		}
	}
	return ddl
}

// A read-only column whose value arrives from a database TRIGGER. The rule's
// own remediation names `COMMENT ON COLUMN ... forge:fill=handler` as the
// declaration for this, so the question here is only whether the declared
// form is actually silent.
func TestReadOnlyFields_TriggerDeclaredByFillMarkerIsSilent(t *testing.T) {
	root := readOnlyProject(t, estimateProto, estimateMigration+`
COMMENT ON COLUMN estimates.total_cents IS 'forge:fill=handler — maintained by the recalc trigger';
`, "")

	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a declared forge:fill=handler must silence the rule, got %+v", findings)
	}
}

// A project may have no db/migrations at all — the schema half is missing,
// so the rule has nothing to correlate and must stay silent rather than
// treat every read-only field as unwritten.
func TestReadOnlyFields_NoMigrationsIsSilent(t *testing.T) {
	root := t.TempDir()
	protoDir := filepath.Join(root, "proto", "services", "estimates", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "estimates.proto"), []byte(estimateProto), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := collectReadOnlyFieldFindings(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("no migrations means no schema half to correlate; want silence, got %+v", findings)
	}
}

// TestReadOnlyFields_ScaffoldedProtoDoesNotGate closes the interaction the
// gating change could otherwise open: a rule that now FAILS the build must
// not fire on what `forge project new` writes, or every new project is red
// on its first `forge lint` — the same day-one defect the frontend
// process-env scaffold fix exists to close.
//
// The scaffolded birth migration and proto stub carry no forge:read-only
// field at all, so the correct assertion is that the rule finds nothing.
func TestReadOnlyFields_ScaffoldedProtoDoesNotGate(t *testing.T) {
	root := readOnlyProject(t, `syntax = "proto3";

package services.tasks.v1;

// forge:entity
message Task {
  string id = 1;
  string title = 2;
  string created_at = 3;
  string updated_at = 4;
}
`, `CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
`, "")

	findings, err := collectReadOnlyFieldsJSON(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a scaffolded entity must not trip a GATING rule — that makes every new "+
			"project's first `forge lint` red. Got %+v", findings)
	}
}
