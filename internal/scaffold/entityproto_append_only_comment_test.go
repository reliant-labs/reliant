// File: internal/scaffold/entityproto_append_only_comment_test.go
//
// The birth migration must DECLARE append-only in the catalog, not only
// enforce it with a trigger. The trigger defends the data at runtime; the
// COMMENT ON TABLE is what every later `forge generate` pass reads, because
// the proto marker is birth-time only and nothing carries it forward.

package scaffold

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Without this declaration the marker dies at birth. The trigger lands, the
// Update/Delete RPCs are omitted, and then every generate-time pass — which
// reads the APPLIED SCHEMA, not the proto — sees an ordinary table. That is
// how an append-only ledger ended up with a generated store exposing
// UpdateX, UpdateXMasked and DeleteX: nothing downstream had any way to
// know.
func TestRenderEntityMigrationFromProto_AppendOnlyDeclaresTableMarker(t *testing.T) {
	mig := RenderEntityMigrationFromProto(hardeningSpec())

	want := "COMMENT ON TABLE audit_logs IS '" + schemadef.TableMarkerAppendOnly
	if !strings.Contains(mig.UpSQL, want) {
		t.Errorf("append-only birth must declare %s on the table, or no post-birth pass can read the marker:\n%s",
			schemadef.TableMarkerAppendOnly, mig.UpSQL)
	}
}

// The declaration is opt-in, exactly as the trigger is.
func TestRenderEntityMigrationFromProto_NoTableMarkerWithoutAppendOnly(t *testing.T) {
	spec := hardeningSpec()
	spec.AppendOnly = false

	if strings.Contains(RenderEntityMigrationFromProto(spec).UpSQL, schemadef.TableMarkerAppendOnly) {
		t.Error("an ordinary entity must carry no append-only declaration")
	}
}

// The round trip is the real contract: pinning the emitted SQL proves only
// that forge writes the string it meant to. What the store generator
// depends on is that the marker comes BACK out of a real postgres catalog
// after the migration is applied — the same path `forge generate`
// introspects through.
func TestAppendOnlyTableMarker_SurvivesTheShadowRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}

	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()

	for _, stmt := range schemadef.SplitStatements(RenderEntityMigrationFromProto(hardeningSpec()).UpSQL) {
		if strings.TrimSpace(stripSQLComments(stmt)) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("append-only migration statement failed to apply:\n%s\nerr: %v", stmt, err)
		}
	}

	tables, err := schemadef.Introspect(db)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var found bool
	for _, tbl := range tables {
		if tbl.Name != "audit_logs" {
			continue
		}
		found = true
		if !tbl.AppendOnly() {
			t.Errorf("the applied table must read back as append-only; got comment %q", tbl.Comment)
		}
		if !schemadef.DetectConventions(tbl).AppendOnly {
			t.Error("DetectConventions must surface append-only — it is what the entity projection reads")
		}
	}
	if !found {
		t.Fatal("audit_logs was not introspected")
	}
}
