package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

func schemaTestEntity(schema string) config.PlanEntity {
	return config.PlanEntity{
		Name:      "Widget",
		TableName: "widgets",
		Schema:    schema,
		Fields:    []config.PlanEntityField{{Name: "id", Type: "string", PrimaryKey: true}},
	}
}

// A table the migrations put outside public is addressed schema-qualified,
// and its alias stays the bare name so column qualification still matches.
// A public (or unknown-schema) table stays bare, which is what keeps a
// search_path-per-tenant project's unqualified tables resolving per tenant.
func TestRenderORMEntityQualifiesNonPublicSchema(t *testing.T) {
	cases := map[string]string{
		"":             "`bun:\"table:widgets,alias:widgets\"`",
		"public":       "`bun:\"table:widgets,alias:widgets\"`",
		"controlplane": "`bun:\"table:controlplane.widgets,alias:widgets\"`",
	}
	for schema, want := range cases {
		code := string(renderORMEntity("db", schemaTestEntity(schema), false))
		if !strings.Contains(code, want) {
			t.Errorf("schema %q: entity struct lacks %s:\n%s", schema, want, code)
		}
		// The TableName constant names the table, never the schema: it is
		// what forge keys entities by everywhere else.
		if !strings.Contains(code, `const WidgetTableName = "widgets"`) {
			t.Errorf("schema %q: WidgetTableName is not the bare table name", schema)
		}
	}
}

// qualifiedWidget carries the exact tag TestRenderORMEntityQualifiesNonPublicSchema
// pins, so the round trip below proves what bun does with that tag.
type qualifiedWidget struct {
	bun.BaseModel `bun:"table:app.widgets,alias:widgets"`
	ID            string `bun:"id,pk"`
	Name          string `bun:"name"`
}

// The tag only helps if bun turns it into SQL that reaches the table when
// the schema is NOT on the search_path. Settle it on a real server: create
// app.widgets, pin search_path to public, and round-trip a row through the
// ORM's own delegates path (bun model queries on an orm.Client).
func TestQualifiedTableTagReachesTableOffSearchPath(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres; skipped under -short")
	}
	dir := t.TempDir()
	mig := `CREATE SCHEMA app;
CREATE TABLE app.widgets (id TEXT PRIMARY KEY, name TEXT NOT NULL);
`
	if err := os.WriteFile(filepath.Join(dir, "00001_widgets.up.sql"), []byte(mig), 0o644); err != nil {
		t.Fatal(err)
	}
	tables, shadow, err := schemadef.ApplyAndIntrospectShadowAt(dir, "")
	if shadow != nil {
		defer shadow.Close()
	}
	if err != nil {
		t.Fatalf("ApplyAndIntrospectShadowAt: %v", err)
	}
	var got schemadef.Table
	for _, tb := range tables {
		if tb.Name == "widgets" {
			got = tb
		}
	}
	if got.Schema != "app" {
		t.Fatalf("introspected widgets in schema %q, want app", got.Schema)
	}
	code := string(renderORMEntity("db", schemaTestEntity(got.Schema), false))
	if !strings.Contains(code, "`bun:\"table:app.widgets,alias:widgets\"`") {
		t.Fatalf("generated tag is not the one this test round-trips:\n%s", code)
	}

	db := shadow.DB()
	db.SetMaxOpenConns(1) // one session, so the SET below holds for every query
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SET search_path = public`); err != nil {
		t.Fatal(err)
	}
	client, err := orm.NewClientWithDB(db, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Bun().NewInsert().Model(&qualifiedWidget{ID: "w1", Name: "one"}).Exec(ctx); err != nil {
		t.Fatalf("insert through the qualified tag: %v", err)
	}
	var w qualifiedWidget
	if err := client.Bun().NewSelect().Model(&w).Where("widgets.id = ?", "w1").Scan(ctx); err != nil {
		t.Fatalf("select through the qualified tag: %v", err)
	}
	if w.Name != "one" {
		t.Fatalf("read back %+v", w)
	}
}
