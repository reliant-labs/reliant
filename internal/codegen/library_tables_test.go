package codegen

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/alerting/pgstore"
	"github.com/reliant-labs/forge/pkg/audit"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Forge bug 22dc63db: control-plane turned the ORM on and generate failed on
// `tables audit_log and audit_logs both project to the entity AuditLog`.
// audit_log is forge/pkg/audit's table; the ORM had no business projecting a
// second store over it.

// libTable is a table with exactly the columns name's library reads and
// writes, keyed by its first column.
func libTable(t *testing.T, name string) schemadef.Table {
	t.Helper()
	for _, lt := range libraryTables {
		if lt.name != name {
			continue
		}
		tbl := schemadef.Table{Name: name, Schema: "controlplane", PKCols: []string{lt.columns[0]}}
		for i, c := range lt.columns {
			tbl.Columns = append(tbl.Columns, schemadef.Column{Name: c, Type: schemadef.CanonicalType("string"), IsPK: i == 0, NotNull: i == 0})
		}
		return tbl
	}
	t.Fatalf("no library table %q", name)
	return schemadef.Table{}
}

// The fixture of the bug: the library's audit_log beside the app's
// audit_logs. The library table is left off; the app's keeps the name.
func TestORMEntities_LibraryTableIsLeftOffAndTheAppTableKeepsItsName(t *testing.T) {
	auditLogs := pkTable("audit_logs", "id")
	auditLogs.Schema = "controlplane"
	got, notes, _, err := ormEntitiesForTables([]schemadef.Table{libTable(t, "audit_log"), auditLogs}, nil, nil)
	if err != nil {
		t.Fatalf("a library table must not collide with the app's table: %v", err)
	}
	if s := strings.Join(entityNames(got), ","); s != "AuditLog=audit_logs" {
		t.Errorf("ORM entities = %s, want only the app's AuditLog=audit_logs", s)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"audit_log is forge/pkg/audit's table", "COMMENT ON TABLE controlplane.audit_log IS 'forge:entity'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes should say %q:\n%s", want, joined)
		}
	}
}

// Every registered library table is left off by default.
func TestORMEntities_EveryLibraryTableIsLeftOff(t *testing.T) {
	var tables []schemadef.Table
	for _, lt := range libraryTables {
		tables = append(tables, libTable(t, lt.name))
	}
	got, notes, _, err := ormEntitiesForTables(tables, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("library tables were projected: %v", entityNames(got))
	}
	if len(notes) != len(libraryTables) {
		t.Errorf("want one note per library table, got:\n%s", strings.Join(notes, "\n"))
	}
}

// The name alone does not make a table the library's: an app table called
// alert_events with its own columns is the app's, and keeps its store.
func TestORMEntities_ATableThatOnlySharesALibraryNameIsTheApps(t *testing.T) {
	got, notes, _, err := ormEntitiesForTables([]schemadef.Table{pkTable("alert_events", "id")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(entityNames(got), ","); s != "AlertEvent=alert_events" {
		t.Errorf("ORM entities = %s, want the app's AlertEvent", s)
	}
	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
}

// What keeps a library table projected: the explicit opt-in, or a CRUD rpc
// whose wiring compiles against its store.
func TestORMEntities_LibraryTableProjectedWhenSomethingDependsOnIt(t *testing.T) {
	t.Run("forge:entity", func(t *testing.T) {
		tbl := libTable(t, "audit_log")
		tbl.Comment = "forge:entity — we read it through the generated store"
		got, notes, _, err := ormEntitiesForTables([]schemadef.Table{tbl}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.Join(entityNames(got), ","); s != "AuditLog=audit_log" || len(notes) != 0 {
			t.Errorf("entities = %s, notes = %v; want AuditLog projected silently", s, notes)
		}
	})
	t.Run("a CRUD rpc", func(t *testing.T) {
		svc := ServiceDef{Name: "ActivityService", Package: "app.v1", Methods: []Method{{Name: "ListActivityEvents"}},
			Messages: map[string][]MessageFieldDef{"ActivityEvent": {{Name: "id", ProtoType: "string"}}}}
		got, _, _, err := ormEntitiesForTables([]schemadef.Table{libTable(t, "activity_events")}, []ServiceDef{svc}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.Join(entityNames(got), ","); s != "ActivityEvent=activity_events" {
			t.Errorf("entities = %s; the CRUD wiring over activity_events needs its store", s)
		}
	})
}

// Generated code is public within major 1: a library table whose store the
// project already generates keeps it, with a deprecation naming the table,
// the next major, and the opt-out migration to apply now.
func TestORMEntities_ExistingLibraryStoreIsKeptAndDeprecated(t *testing.T) {
	had := func(tbl schemadef.Table, entity string) (string, bool) {
		return "internal/store/alert_event_orm_gen.go", tbl.Name == "alert_events" && entity == "AlertEvent"
	}
	got, notes, deprecations, err := ormEntitiesForTables([]schemadef.Table{libTable(t, "alert_events"), libTable(t, "audit_log")}, nil, had)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(entityNames(got), ","); s != "AlertEvent=alert_events" {
		t.Errorf("entities = %s; the store the project already generates must be kept, the new table excluded", s)
	}
	if len(deprecations) != 1 {
		t.Fatalf("want one deprecation, got %q", deprecations)
	}
	for _, want := range []string{
		"deprecated: alert_events is forge/pkg/alerting/pgstore's table",
		"internal/store/alert_event_orm_gen.go",
		"stops being generated in the next major version",
		"`COMMENT ON TABLE controlplane.alert_events IS 'forge:no-entity'`",
	} {
		if !strings.Contains(deprecations[0], want) {
			t.Errorf("deprecation should say %q:\n%s", want, deprecations[0])
		}
	}
	// audit_log has no store yet: excluded at once, with the plain note.
	if joined := strings.Join(notes, "\n"); !strings.Contains(joined, "audit_log is forge/pkg/audit's table") || strings.Contains(joined, "alert_events") {
		t.Errorf("notes = %q; want only audit_log's exclusion note", notes)
	}
}

// A kept store never beats another table's name: a projection that collides
// could not have compiled, so the library table yields, without a warning.
func TestORMEntities_ExistingLibraryStoreYieldsToTheAppsTable(t *testing.T) {
	had := func(schemadef.Table, string) (string, bool) { return "internal/db/audit_log_orm_gen.go", true }
	got, _, deprecations, err := ormEntitiesForTables([]schemadef.Table{libTable(t, "audit_log"), pkTable("audit_logs", "id")}, nil, had)
	if err != nil {
		t.Fatalf("the library table must yield, not collide: %v", err)
	}
	if s := strings.Join(entityNames(got), ","); s != "AuditLog=audit_logs" || len(deprecations) != 0 {
		t.Errorf("entities = %s, deprecations = %q; want the app's audit_logs alone", s, deprecations)
	}
}

func TestExistingORMProjection_ReadsTheSourceTable(t *testing.T) {
	dir := t.TempDir()
	write := func(name, table string) {
		body := "// Code generated by forge. DO NOT EDIT.\n// Source: the APPLIED schema (db/migrations) of table " + table + ".\npackage store\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("audit_log_orm_gen.go", "audit_log")
	write("alert_event_orm_gen.go", "controlplane.alert_events")
	write("activity_event_orm_gen.go", "something_else")
	projected := existingORMProjection(dir, "internal/store")

	if file, ok := projected(schemadef.Table{Name: "audit_log", Schema: "controlplane"}, "AuditLog"); !ok || file != "internal/store/audit_log_orm_gen.go" {
		t.Errorf("projected(audit_log) = %q, %v", file, ok)
	}
	if _, ok := projected(schemadef.Table{Name: "alert_events", Schema: "controlplane"}, "AlertEvent"); !ok {
		t.Error("a schema-qualified source names the same table")
	}
	if _, ok := projected(schemadef.Table{Name: "activity_events"}, "ActivityEvent"); ok {
		t.Error("a file projected from another table is not this table's store")
	}
	if _, ok := projected(schemadef.Table{Name: "alert_instances"}, "AlertInstance"); ok {
		t.Error("no file, no store")
	}
}

// Two tables one name is a typed error naming both tables and both fixes,
// with the tables addressed the way a migration must spell them.
func TestORMEntities_CollisionIsTypedAndSchemaQualified(t *testing.T) {
	a, b := pkTable("widget", "id"), pkTable("widgets", "id")
	a.Schema, b.Schema = "controlplane", "controlplane"
	_, _, _, err := ormEntitiesForTables([]schemadef.Table{b, a}, nil, nil)
	var collision *EntityCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v (%T), want *EntityCollisionError", err, err)
	}
	if collision.First.Name != "widget" || collision.Second.Name != "widgets" || collision.Entity != "Widget" {
		t.Errorf("collision = %s / %s → %s", collision.First.Name, collision.Second.Name, collision.Entity)
	}
	for _, want := range []string{
		"tables widget and widgets both project to the entity Widget",
		"COMMENT ON TABLE controlplane.widgets IS 'forge:entity-name=<Name>'",
		"COMMENT ON TABLE controlplane.widget IS 'forge:no-entity'",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("collision error should say %q:\n%v", want, err)
		}
	}

	// A library table its project opted in collides like any other.
	opted := libTable(t, "audit_log")
	opted.Comment = "forge:entity"
	if _, _, _, err := ormEntitiesForTables([]schemadef.Table{opted, pkTable("audit_logs", "id")}, nil, nil); !errors.As(err, &collision) {
		t.Errorf("an opted-in library table must still be refused on a collision, got %v", err)
	}
}

// The registry is the libraries' own schema: a column a library starts
// reading or writing must reach its row, or a table with the new shape stops
// being recognized (and one with the old shape would be claimed wrongly).
func TestLibraryTables_MatchTheLibrariesSchemas(t *testing.T) {
	want := map[string][]string{}

	var auditCols []string
	et := reflect.TypeOf(audit.Entry{})
	for i := 0; i < et.NumField(); i++ {
		if tag := et.Field(i).Tag.Get("db"); tag != "" && tag != "-" {
			auditCols = append(auditCols, tag)
		}
	}
	want["audit_log"] = auditCols

	for table, cols := range ddlColumns(t, activity.SchemaSQL) {
		want[table] = cols
	}
	alerting, err := pgstore.SchemaSQL("")
	if err != nil {
		t.Fatal(err)
	}
	for table, cols := range ddlColumns(t, alerting) {
		want[table] = cols
	}

	if len(want) != len(libraryTables) {
		t.Errorf("the libraries declare %d tables, libraryTables registers %d", len(want), len(libraryTables))
	}
	for _, lt := range libraryTables {
		cols, ok := want[lt.name]
		if !ok {
			t.Errorf("libraryTables registers %s, which no library declares", lt.name)
			continue
		}
		got, exp := slices.Clone(lt.columns), slices.Clone(cols)
		slices.Sort(got)
		slices.Sort(exp)
		if !slices.Equal(got, exp) {
			t.Errorf("%s: libraryTables lists %v, the library's schema has %v", lt.name, got, exp)
		}
	}
}

var (
	createTableRE = regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+) \((.*?)\n\);`)
	columnLineRE  = regexp.MustCompile(`(?m)^\s+([a-z_]+)\s+[A-Za-z]`)
	addColumnRE   = regexp.MustCompile(`(?i)ALTER TABLE ([a-z_]+) ADD COLUMN (?:IF NOT EXISTS )?([a-z_]+)`)
)

// ddlColumns reads each table's columns out of a library's DDL.
func ddlColumns(t *testing.T, ddl string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, m := range createTableRE.FindAllStringSubmatch(ddl, -1) {
		for _, c := range columnLineRE.FindAllStringSubmatch(m[2], -1) {
			switch strings.ToUpper(c[1]) {
			case "PRIMARY", "CONSTRAINT", "UNIQUE", "CHECK", "FOREIGN":
				continue
			}
			out[m[1]] = append(out[m[1]], c[1])
		}
	}
	for _, m := range addColumnRE.FindAllStringSubmatch(ddl, -1) {
		if !slices.Contains(out[m[1]], m[2]) { // an upgrade path re-adding a born column
			out[m[1]] = append(out[m[1]], m[2])
		}
	}
	if len(out) == 0 {
		t.Fatalf("no CREATE TABLE in:\n%s", ddl)
	}
	return out
}
