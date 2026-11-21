package codegen

import (
	"errors"
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
	got, notes, err := ormEntitiesForTables([]schemadef.Table{libTable(t, "audit_log"), auditLogs}, nil)
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
	got, notes, err := ormEntitiesForTables(tables, nil)
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
	got, notes, err := ormEntitiesForTables([]schemadef.Table{pkTable("alert_events", "id")}, nil)
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
		got, notes, err := ormEntitiesForTables([]schemadef.Table{tbl}, nil)
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
		got, _, err := ormEntitiesForTables([]schemadef.Table{libTable(t, "activity_events")}, []ServiceDef{svc})
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.Join(entityNames(got), ","); s != "ActivityEvent=activity_events" {
			t.Errorf("entities = %s; the CRUD wiring over activity_events needs its store", s)
		}
	})
}

// Two tables one name is a typed error naming both tables and both fixes,
// with the tables addressed the way a migration must spell them.
func TestORMEntities_CollisionIsTypedAndSchemaQualified(t *testing.T) {
	a, b := pkTable("widget", "id"), pkTable("widgets", "id")
	a.Schema, b.Schema = "controlplane", "controlplane"
	_, _, err := ormEntitiesForTables([]schemadef.Table{b, a}, nil)
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
	if _, _, err := ormEntitiesForTables([]schemadef.Table{opted, pkTable("audit_logs", "id")}, nil); !errors.As(err, &collision) {
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
