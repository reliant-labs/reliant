package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

func noSeedFixture() []schemadef.Table {
	col := func(name string, notNull bool) schemadef.Column {
		return schemadef.Column{Name: name, NotNull: notNull}
	}
	fk := func(c, ref string) schemadef.ForeignKey {
		return schemadef.ForeignKey{Column: c, RefTable: ref, RefColumn: "id"}
	}
	return []schemadef.Table{
		{Name: "jobs", Columns: []schemadef.Column{col("id", true), col("parent_id", false)},
			ForeignKeys: []schemadef.ForeignKey{fk("parent_id", "jobs")}},
		// Marked, with a reason after the marker.
		{Name: "job_leases", Comment: "forge:no-seed — the control plane writes leases",
			Columns: []schemadef.Column{col("job_id", true)}, ForeignKeys: []schemadef.ForeignKey{fk("job_id", "jobs")}},
		// Requires a lease: cannot exist without one, so it is out too.
		{Name: "lease_audits", Columns: []schemadef.Column{col("id", true), col("lease_id", true)},
			ForeignKeys: []schemadef.ForeignKey{fk("lease_id", "job_leases")}},
		// ...and transitively.
		{Name: "lease_audit_notes", Columns: []schemadef.Column{col("id", true), col("audit_id", true)},
			ForeignKeys: []schemadef.ForeignKey{fk("audit_id", "lease_audits")}},
		// An OPTIONAL link into the marked table keeps the table: written NULL.
		{Name: "events", Columns: []schemadef.Column{col("id", true), col("lease_id", false)},
			ForeignKeys: []schemadef.ForeignKey{fk("lease_id", "job_leases")}},
		// A near-miss marker name is a different token, not an opt-out.
		{Name: "seeds", Comment: "forge:no-seeds", Columns: []schemadef.Column{col("id", true)}},
	}
}

func TestExcludeNoSeed_MarkedTablesAndTheirRequiredChildren(t *testing.T) {
	kept, warns := ExcludeNoSeed(noSeedFixture())
	if got, want := keptNames(kept), "jobs, events, seeds"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
	want := []string{
		"seed plan: lease_audit_notes not seeded: its NOT NULL foreign key audit_id references lease_audits, which is not seeded (forge:no-seed)",
		"seed plan: lease_audits not seeded: its NOT NULL foreign key lease_id references job_leases, which is not seeded (forge:no-seed)",
	}
	if !reflect.DeepEqual(warns, want) {
		t.Errorf("warnings =\n  %s\nwant\n  %s", strings.Join(warns, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestExcludeNoSeed_NoMarkerKeepsEverything(t *testing.T) {
	tables := []schemadef.Table{{Name: "jobs", Comment: "forge:no-entity"}}
	if kept, warns := ExcludeNoSeed(tables); len(kept) != 1 || warns != nil {
		t.Errorf("forge:no-entity is not forge:no-seed: kept %v, warnings %v", keptNames(kept), warns)
	}
}

// The marker shares a comment with other markers.
func TestTableNoSeed_ReadsAWholeToken(t *testing.T) {
	for comment, want := range map[string]bool{
		"forge:no-seed":                  true,
		"forge:no-seed — app-owned":      true,
		"forge:no-entity forge:no-seed":  true,
		"forge:no-seeds":                 false,
		"forge:no-seed-data":             false,
		"forge:no-entity — not a seeder": false,
	} {
		if got := (schemadef.Table{Comment: comment}).NoSeed(); got != want {
			t.Errorf("NoSeed(%q) = %v, want %v", comment, got, want)
		}
	}
	if err := schemadef.CheckMarkers([]schemadef.Table{{Name: "job_leases", Comment: "forge:no-entity forge:no-seed — app-owned"}}); err != nil {
		t.Errorf("forge:no-seed must be a known table marker: %v", err)
	}
}

// noSeedMigration is friction item #3's shape: a CRUD table, and beside it
// control-plane state its own code writes, one table carrying a serialized
// protobuf the app decodes.
const noSeedMigration = `
CREATE TABLE jobs (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE assignments (
    job_id     TEXT   NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    generation BIGINT NOT NULL CHECK (generation > 0),
    payload    BYTEA  NOT NULL,
    PRIMARY KEY (job_id, generation)
);
COMMENT ON TABLE assignments IS 'forge:no-seed — payload is a serialized shared.v1.Assignment';
CREATE TABLE assignment_acks (
    id         TEXT   PRIMARY KEY,
    job_id     TEXT   NOT NULL,
    generation BIGINT NOT NULL,
    FOREIGN KEY (job_id, generation) REFERENCES assignments (job_id, generation)
);
CREATE TABLE job_leases (
    job_id TEXT PRIMARY KEY REFERENCES jobs (id) ON DELETE CASCADE,
    holder TEXT NOT NULL
);
COMMENT ON TABLE job_leases IS 'forge:no-seed';
CREATE TABLE notes (
    id       TEXT PRIMARY KEY,
    job_id   TEXT NOT NULL REFERENCES jobs (id),
    lease_id TEXT REFERENCES job_leases (job_id)
);
`

// TestMaterialize_NoSeedTableGetsNoRows is the real-postgres proof: an
// unscoped `forge db seed apply` (every table) writes no row into a
// forge:no-seed table or one that requires it, and still seeds the rest.
func TestMaterialize_NoSeedTableGetsNoRows(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(noSeedMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(noSeedMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 5})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	if got, want := plan.NoSeedTables(), []string{"assignment_acks", "assignments", "job_leases"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NoSeedTables = %v, want %v", got, want)
	}
	if w := strings.Join(plan.Warnings(), "\n"); !strings.Contains(w, "assignment_acks not seeded") {
		t.Errorf("warnings do not name the table left out for requiring a no-seed one:\n%s", w)
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertCount(t, db, "jobs", 5)
	assertCount(t, db, "notes", 5)
	assertCount(t, db, "assignments", 0)
	assertCount(t, db, "assignment_acks", 0)
	assertCount(t, db, "job_leases", 0)
	var linked int
	if err := db.QueryRow(`SELECT count(*) FROM notes WHERE lease_id IS NOT NULL`).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 0 {
		t.Errorf("%d notes reference a lease that was never seeded; the optional link must be NULL", linked)
	}
}

// Reset must not let TRUNCATE expand the plan into application-owned tables,
// even when their FK uses ON DELETE CASCADE. PostgreSQL must refuse atomically.
func TestReset_RefusesReferencesOutsidePlan(t *testing.T) {
	requirePG(t)
	for _, scoped := range []bool{false, true} {
		name := "no-seed"
		if scoped {
			name = "scoped"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, cleanup, err := pgtest.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			migration := noSeedMigration
			if scoped {
				// Prove the same protection for an unmarked, out-of-scope table.
				migration = strings.ReplaceAll(migration, "forge:no-seed", "app-owned")
			}
			if _, err := db.Exec(migration + `
INSERT INTO jobs VALUES ('real-job', 'Application job');
INSERT INTO assignments VALUES ('real-job', 1, decode('deadbeef', 'hex'));
INSERT INTO assignment_acks VALUES ('real-ack', 'real-job', 1);
INSERT INTO job_leases VALUES ('real-job', 'real-holder');
INSERT INTO notes VALUES ('real-note', 'real-job', 'real-job');
`); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Rows: 5}
			if scoped {
				cfg.Tables = []string{"jobs", "notes"}
			}
			plan, err := BuildPlanFromDB(ctx, db, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Reset(ctx, db, plan); err == nil || !strings.Contains(err.Error(), "outside the seed plan") {
				t.Fatalf("Reset must refuse references outside the seed plan, got %v", err)
			}
			for _, table := range []string{"jobs", "notes", "assignments", "assignment_acks", "job_leases"} {
				assertCount(t, db, table, 1)
			}
			var payload string
			if err := db.QueryRow(`SELECT encode(payload, 'hex') FROM assignments WHERE job_id = 'real-job'`).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			if payload != "deadbeef" {
				t.Fatalf("application payload changed: %s", payload)
			}
		})
	}
}

func keptNames(tables []schemadef.Table) string {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}
