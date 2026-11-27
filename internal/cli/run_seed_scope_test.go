package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// TestAutoSeedConfig_DefaultsToCRUDEntityTables pins the auto-seed default:
// with database.seed.tables unset, first-boot seeding is scoped to the tables
// behind CRUD entities. It used to write 20 rows into EVERY table — Bark
// Social's founding_reservations got "paid" deposits nobody paid.
func TestAutoSeedConfig_DefaultsToCRUDEntityTables(t *testing.T) {
	got := autoSeedConfig(seedplan.DefaultConfig(), func() []string { return []string{"tasks"} })
	if !reflect.DeepEqual(got.Tables, []string{"tasks"}) {
		t.Errorf("auto-seed scope = %v, want [tasks]", got.Tables)
	}
}

// A project with RPCs but no CRUD entities (Bark Social's membership service)
// scopes to NOTHING, not to everything.
func TestAutoSeedConfig_NoEntitiesSeedsNothing(t *testing.T) {
	got := autoSeedConfig(seedplan.DefaultConfig(), func() []string { return []string{} })
	if got.Tables == nil || len(got.Tables) != 0 {
		t.Errorf("no CRUD entities must scope to an EMPTY set, got %#v", got.Tables)
	}
}

// When forge cannot read the descriptor it must not silently seed nothing on
// a project that simply has not generated yet — the old behaviour stands.
func TestAutoSeedConfig_UnknownEntitiesKeepsEveryTable(t *testing.T) {
	got := autoSeedConfig(seedplan.DefaultConfig(), func() []string { return nil })
	if got.Tables != nil {
		t.Errorf("unknown entity set must leave the scope unset, got %v", got.Tables)
	}
}

// An explicit scope on the base config wins over the entity-derived default.
func TestAutoSeedConfig_ExplicitTablesWin(t *testing.T) {
	base := seedplan.DefaultConfig()
	base.Tables = []string{"plans"}
	got := autoSeedConfig(base, func() []string { t.Fatal("entity lookup must not run"); return nil })
	if !reflect.DeepEqual(got.Tables, []string{"plans"}) {
		t.Errorf("explicit scope lost: %v", got.Tables)
	}
}

// noEntityLookup is the entity-table lookup for an autoSeedFirstBoot whose
// config already names its tables: autoSeedConfig must never consult it.
func noEntityLookup(t *testing.T) func([]schemadef.Table) []string {
	return func([]schemadef.Table) []string {
		t.Helper()
		t.Fatal("entity lookup must not run when database.seed.tables is set")
		return nil
	}
}

// rangerCoreMigration is Ranger's schema in miniature: one CRUD entity (jobs)
// and, beside it, control-plane state its own code writes — assignments
// (forge:no-entity, composite key, a serialized protobuf payload) and
// job_leases (forge:no-entity), each REFERENCING jobs.
const rangerCoreMigration = `
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
COMMENT ON TABLE assignments IS 'forge:no-entity';
CREATE TABLE job_leases (
    job_id     TEXT        PRIMARY KEY REFERENCES jobs (id) ON DELETE CASCADE,
    holder     TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
COMMENT ON TABLE job_leases IS 'forge:no-entity';
`

// rangerServices are Ranger's rpcs over that schema: the jobs CRUD quintet,
// plus CRUD-NAMED rpcs that are not CRUD to the join — GetAssignment over a
// forge:no-entity table, ListGenerations and GetJobStatus over no table.
func rangerServices() []codegen.ServiceDef {
	return []codegen.ServiceDef{
		{Name: "JobsService", Package: "services.jobs.v1",
			Methods:  []codegen.Method{{Name: "CreateJob"}, {Name: "GetJob"}, {Name: "UpdateJob"}, {Name: "DeleteJob"}, {Name: "ListJobs"}, {Name: "GetJobStatus"}},
			Messages: map[string][]codegen.MessageFieldDef{"Job": {{Name: "id", ProtoType: "string"}}}},
		{Name: "AssignmentsService", Package: "services.assignments.v1",
			Methods:  []codegen.Method{{Name: "GetAssignment"}, {Name: "ListGenerations"}},
			Messages: map[string][]codegen.MessageFieldDef{"Assignment": {{Name: "job_id", ProtoType: "string"}}}},
	}
}

// TestAutoSeedFirstBoot_SeedsOnlyCRUDEntityTables is friction item #3's
// regression: `forge env up dev` wrote 20 rows into Ranger's assignments,
// whose synthesized payloads the control plane failed to decode every five
// seconds. The auto-seed matched rpc NAMES to tables, so a lone
// `GetAssignment` rpc made the forge:no-entity assignments table a seed root.
// The scope is the CRUD join's: jobs is seeded, and the tables that merely
// reference it are left empty.
func TestAutoSeedFirstBoot_SeedsOnlyCRUDEntityTables(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; runs in task test")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(rangerCoreMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	migDir := filepath.Join(t.TempDir(), "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(rangerCoreMigration), 0o644); err != nil {
		t.Fatal(err)
	}

	autoSeedFirstBoot(context.Background(), db, migDir, "", seedplan.DefaultConfig(), func(tables []schemadef.Table) []string {
		return codegen.CRUDEntityTables(tables, rangerServices())
	})

	for table, want := range map[string]int{"jobs": 20, "assignments": 0, "job_leases": 0} {
		var got int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s holds %d rows after the first-boot seed, want %d", table, got, want)
		}
	}
}
