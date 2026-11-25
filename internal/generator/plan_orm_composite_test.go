package generator

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// A composite-key table gets a full store addressed by its WHOLE key
// (reliant P5b, F12.3: message_order_counters, project_daemons,
// connection_secrets, …). It used to get no entity at all.
func TestPlanORM_CompositeKeyDelegatesTakeTheWholeKey(t *testing.T) {
	ent := config.PlanEntity{
		Name: "ProjectDaemon", TableName: "project_daemons",
		Fields: []config.PlanEntityField{
			{Name: "project_id", Type: "string", PrimaryKey: true, NotNull: true},
			{Name: "type", Type: "string", PrimaryKey: true, NotNull: true},
			{Name: "note", Type: "string"},
		},
	}
	src := string(renderORMEntity("db", ent, false))
	for _, want := range []string{
		"func GetProjectDaemonByPK(ctx context.Context, db orm.Context, projectID string, typeKey string) (*ProjectDaemon, error) {",
		"projectDaemonRepo.GetByPK(ctx, db, &ProjectDaemon{ProjectID: projectID, Type: typeKey})",
		"func DeleteProjectDaemonByPK(ctx context.Context, db orm.Context, projectID string, typeKey string) error {",
		"func UpsertProjectDaemon(ctx context.Context, db orm.Context, msg *ProjectDaemon) error {",
		"func UpdateProjectDaemon(ctx context.Context, db orm.Context, msg *ProjectDaemon, opts ...crud.UpdateOption) error {",
		"func ListProjectDaemon(",
		"func CountProjectDaemon(",
		"func CreateProjectDaemon(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("composite-key ORM lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "ByID(") {
		t.Errorf("a composite key has no single id to Get/Delete by:\n%s", src)
	}

	store := string(renderEntityStoreFile("db", ent))
	if strings.Contains(store, "\"time\"") {
		t.Errorf("a store keyed by strings imports time, which would not compile:\n%s", store)
	}
	for _, want := range []string{
		"GetProjectDaemonByPK(ctx context.Context, projectID string, typeKey string) (*ProjectDaemon, error)",
		"DeleteProjectDaemonByPK(ctx context.Context, projectID string, typeKey string) error",
		"UpsertProjectDaemon(ctx context.Context, msg *ProjectDaemon) error",
		"var _ ProjectDaemonStore = projectDaemonStore{}",
	} {
		if !strings.Contains(store, want) {
			t.Errorf("composite-key store lacks %q:\n%s", want, store)
		}
	}
}

// A key column of a TIMESTAMPTZ type puts time.Time in the store's by-key
// signatures, so the store file imports time. It once imported only
// context/crud/orm, and `forge generate` wrote a store that did not build
// (control-plane's compute_allowance_consumption: undefined: time).
func TestPlanORM_TimeKeyedStoreImportsTime(t *testing.T) {
	for _, ent := range []config.PlanEntity{
		{
			Name: "AllowanceConsumption", TableName: "allowance_consumptions",
			Fields: []config.PlanEntityField{
				{Name: "org_id", Type: "string", PrimaryKey: true, NotNull: true},
				{Name: "period_start", Type: "time", PrimaryKey: true, NotNull: true},
				{Name: "used", Type: "int64", NotNull: true},
			},
		},
		{
			Name: "Snapshot", TableName: "snapshots",
			Fields: []config.PlanEntityField{
				{Name: "taken_at", Type: "time", PrimaryKey: true, NotNull: true},
				{Name: "label", Type: "string"},
			},
		},
	} {
		store := string(renderEntityStoreFile("db", ent))
		if !strings.Contains(store, "time.Time") {
			t.Fatalf("%s: the fixture no longer puts time.Time in a store signature:\n%s", ent.Name, store)
		}
		if !strings.Contains(store, "\t\"time\"\n") {
			t.Errorf("%s: store names time.Time but does not import time:\n%s", ent.Name, store)
		}
	}
}
