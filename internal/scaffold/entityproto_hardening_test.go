// File: internal/scaffold/entityproto_hardening_test.go
//
// Unit + shadow-round-trip tests for the schema-hardening defaults the
// birth migration carries: the `// forge:append-only` DB guard and the
// `// forge:soft-delete` deleted_at column. All of it lands in the
// USER-OWNED birth migration — these tests pin the emitted SQL and prove it
// survives the same real postgres `forge generate` introspects.

package scaffold

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// hardeningSpec is a minimal entity spec used by the hardening tests
// (append-only guard).
func hardeningSpec() EntityFromProtoSpec {
	return EntityFromProtoSpec{
		Table:     "audit_logs",
		MessageFQ: entityProtoPkg + ".AuditLog",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			{Name: "region", Kind: "string"},
			{Name: "action", Kind: "string"},
		},
		Timestamps: true,
		AppendOnly: true,
	}
}

func TestRenderEntityMigrationFromProto_AppendOnlySQL(t *testing.T) {
	mig := RenderEntityMigrationFromProto(hardeningSpec())

	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION audit_logs_forbid_mutation() RETURNS trigger",
		"RAISE EXCEPTION 'table audit_logs is append-only",
		"CREATE TRIGGER audit_logs_append_only",
		"BEFORE UPDATE OR DELETE ON audit_logs",
	} {
		if !strings.Contains(mig.UpSQL, want) {
			t.Errorf("up SQL missing append-only guard fragment %q:\n%s", want, mig.UpSQL)
		}
	}
}

// A non-append-only entity gets no append-only guard — the default is opt-in
// on the marker, never emitted blindly.
func TestRenderEntityMigrationFromProto_NoHardeningByDefault(t *testing.T) {
	spec := EntityFromProtoSpec{
		Table:     "widgets",
		MessageFQ: entityProtoPkg + ".Widget",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			{Name: "name", Kind: "string"},
		},
		Timestamps: true,
	}
	mig := RenderEntityMigrationFromProto(spec)
	if strings.Contains(mig.UpSQL, "forbid_mutation") {
		t.Errorf("no append-only guard expected without the marker:\n%s", mig.UpSQL)
	}
}

// TestRenderEntityMigrationFromProto_AppendOnlyGuardEnforced is the real
// contract for the append-only marker: the emitted trigger must apply to
// the SAME real postgres `forge generate` uses AND actually reject every
// UPDATE/DELETE (INSERT still works). A `DO INSTEAD NOTHING` rule would
// silently swallow the write; the trigger raises — this test proves it.
func TestRenderEntityMigrationFromProto_AppendOnlyGuardEnforced(t *testing.T) {
	if testing.Short() {
		t.Skip("append-only postgres enforcement skipped in -short mode")
	}
	mig := RenderEntityMigrationFromProto(hardeningSpec())

	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()

	// Apply EVERY statement strictly (schemadef's shadow apply is
	// best-effort for triggers; here we assert the guard installs cleanly).
	for _, stmt := range schemadef.SplitStatements(mig.UpSQL) {
		if strings.TrimSpace(stripSQLComments(stmt)) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("append-only migration statement failed to apply:\n%s\nerr: %v", stmt, err)
		}
	}

	// INSERT is permitted (append is the whole point).
	if _, err := db.Exec(`INSERT INTO audit_logs (id, region, action) VALUES ('a1', 't1', 'created')`); err != nil {
		t.Fatalf("INSERT into an append-only table must succeed: %v", err)
	}
	// UPDATE and DELETE are rejected LOUDLY.
	if _, err := db.Exec(`UPDATE audit_logs SET action = 'tamper' WHERE id = 'a1'`); err == nil {
		t.Error("UPDATE on an append-only table must be rejected, but it succeeded")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE rejection should name the append-only guard, got: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_logs WHERE id = 'a1'`); err == nil {
		t.Error("DELETE on an append-only table must be rejected, but it succeeded")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE rejection should name the append-only guard, got: %v", err)
	}

	// The row is still there, unchanged.
	var action string
	if err := db.QueryRow(`SELECT action FROM audit_logs WHERE id = 'a1'`).Scan(&action); err != nil {
		t.Fatalf("row should still exist after the rejected mutations: %v", err)
	}
	if action != "created" {
		t.Errorf("row must be unchanged; action = %q, want %q", action, "created")
	}
}

// stripSQLComments drops leading -- line comments so a comment-only
// "statement" segment (banner lines) isn't fed to Exec.
func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
