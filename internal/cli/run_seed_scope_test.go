package cli

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
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

func TestCRUDEntityTables(t *testing.T) {
	services := []codegen.ServiceDef{{Methods: []codegen.Method{
		{Name: "CreateTask"}, {Name: "ListTasks"}, {Name: "GetTask"},
		{Name: "ListUsageEvents"},
		{Name: "JoinWaitlist"}, // not CRUD
		{Name: "WatchTasks", ServerStreaming: true},
	}}}
	got := crudEntityTables(services)
	// Each entity names its conventional plural and its singular table
	// (codegen.EntityTableCandidates); the one that does not exist is
	// ignored by seedplan.ScopeTables.
	if want := []string{"tasks", "task", "usage_events", "usage_event"}; !reflect.DeepEqual(got, want) {
		t.Errorf("crudEntityTables = %v, want %v", got, want)
	}
}
