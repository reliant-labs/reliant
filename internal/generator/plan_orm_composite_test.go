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

	store := string(renderStoreInterfaces("db", []config.PlanEntity{ent}))
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
