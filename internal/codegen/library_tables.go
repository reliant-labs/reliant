package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// libraryTable is a table a forge library reads and writes through a store
// of its own. The project owns the migration (forge never applies one
// behind its back), but not the data path: forge/pkg/audit's DBAuditStore is
// THE writer of audit_log. A generated ORM store beside it is a second,
// unaudited write path, and its entity name collides with any app table
// that singularizes the same way — control-plane's audit_logs is the
// measured case (forge bug 22dc63db: `tables audit_log and audit_logs both
// project to the entity AuditLog`, after the app had done nothing but turn
// the ORM on).
//
// A table is the library's only when its NAME and the library's COLUMNS
// both match: an app table that merely happens to be called alert_events,
// with its own columns, is the app's and keeps its store. The column lists
// are pinned to each library's own schema by library_tables_test.go, so
// "the library knows its tables" stays true as the libraries evolve.
type libraryTable struct {
	name    string   // the unqualified table name, in whatever schema the project put it
	library string   // the import path of the library that owns it
	columns []string // every column the library's store reads or writes
}

// libraryTables is every table a forge library owns. Add a row when a
// library gains a table: the pinning test fails a row whose columns drift
// from the library's schema, not a library nobody registered, so a new
// library store must be added here by hand.
var libraryTables = []libraryTable{
	{
		name:    "audit_log",
		library: "github.com/reliant-labs/forge/pkg/audit",
		columns: []string{"id", "timestamp", "user_id", "email", "procedure", "peer_address", "duration_ms", "status", "error_code", "error_message", "metadata", "created_at"},
	},
	{
		name:    "activity_events",
		library: "github.com/reliant-labs/forge/pkg/activity",
		columns: []string{"id", "occurred_at", "scope", "actor_id", "actor_name", "actor_kind", "verb", "target_kind", "target_id", "target_label", "env", "via", "procedure", "detail", "trace_id"},
	},
	{
		name:    "alert_instances",
		library: "github.com/reliant-labs/forge/pkg/alerting/pgstore",
		columns: []string{"owner", "env", "rule", "fingerprint", "labels", "annotations", "state", "active_at", "firing_at", "cleared_at", "resolved_at", "resolve_sent", "last_eval_at", "last_value", "why"},
	},
	{
		name:    "alert_events",
		library: "github.com/reliant-labs/forge/pkg/alerting/pgstore",
		columns: []string{"id", "owner", "env", "rule", "fingerprint", "labels", "transition", "value", "at", "why"},
	},
}

// libraryOwning returns the forge library that owns t: t carries one of
// libraryTables' names and every column that library's store reads or
// writes. A view is never a library's table.
func libraryOwning(t schemadef.Table) (libraryTable, bool) {
	if t.IsView {
		return libraryTable{}, false
	}
	for _, lt := range libraryTables {
		if lt.name != t.Name {
			continue
		}
		have := make([]string, 0, len(t.Columns))
		for _, c := range t.Columns {
			have = append(have, c.Name)
		}
		for _, want := range lt.columns {
			if !slices.Contains(have, want) {
				return libraryTable{}, false
			}
		}
		return lt, true
	}
	return libraryTable{}, false
}

// shortLibrary is the library's path as the docs spell it: forge/pkg/audit.
func (lt libraryTable) shortLibrary() string {
	return strings.TrimPrefix(lt.library, "github.com/reliant-labs/")
}

// ormProjected reports whether the project's ORM package already holds the
// generated store of table t (projected as entity), and the file that says
// so. A library table with one keeps it until the next major: generated code
// is public API within major 1, so a store a project already generates is
// deprecated before it is removed, whether or not anything calls it today.
// A nil ormProjected means no store exists anywhere.
type ormProjected func(t schemadef.Table, entity string) (file string, ok bool)

// ormSourceTableRE reads the table an <entity>_orm_gen.go was projected
// from, out of the header the ORM writes: "Source: the APPLIED schema
// (db/migrations) of table connector_audit_log."
var ormSourceTableRE = regexp.MustCompile(`(?m)^// Source: the APPLIED schema \(db/migrations\) of table ([A-Za-z0-9_."]+)\.\s*$`)

// existingORMProjection is the ormProjected that reads the committed
// <entity>_orm_gen.go files in ormDir (absolute). relDir is the same
// directory relative to the project, for messages. A file counts only when
// its header names t as its source, so an entity file another table
// projects to is not mistaken for t's.
func existingORMProjection(ormDir, relDir string) ormProjected {
	return func(t schemadef.Table, entity string) (string, bool) {
		name := naming.ToSnakeCase(entity) + "_orm_gen.go"
		body, err := os.ReadFile(filepath.Join(ormDir, name))
		if err != nil {
			return "", false
		}
		m := ormSourceTableRE.FindSubmatch(body)
		if m == nil {
			return "", false
		}
		src := strings.ReplaceAll(string(m[1]), `"`, "")
		if src != t.Name && src != t.Schema+"."+t.Name {
			return "", false
		}
		return filepath.ToSlash(filepath.Join(relDir, name)), true
	}
}

// tableRef is t as a migration should name it in a COMMENT ON TABLE:
// schema-qualified when it lives outside public, because a bare name
// resolves through the migrator's search_path and a table in another
// schema is then "does not exist".
func tableRef(t schemadef.Table) string {
	if t.Schema == "" || t.Schema == "public" {
		return quoteIdentIfNeeded(t.Name)
	}
	return quoteIdentIfNeeded(t.Schema) + "." + quoteIdentIfNeeded(t.Name)
}

var plainIdentRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func quoteIdentIfNeeded(s string) string {
	if plainIdentRE.MatchString(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// libraryTableNote is the one-line reason a library table is off the ORM.
func libraryTableNote(t schemadef.Table, lt libraryTable) string {
	return fmt.Sprintf("%s is %s's table, read and written through that library's own store, so forge projects no ORM store over it "+
		"(`COMMENT ON TABLE %s IS '%s'` projects one anyway; '%s' says you meant to leave it off and silences this note).",
		t.Name, lt.shortLibrary(), tableRef(t), schemadef.ViewMarkerEntity, schemadef.TableMarkerNoEntity)
}

// libraryTableDeprecation is the warning for a library table whose store
// the project already generates: kept for the rest of major 1, removed in
// the next, with the opt-out to apply now.
func libraryTableDeprecation(t schemadef.Table, lt libraryTable, file string) string {
	return fmt.Sprintf("deprecated: %s is %s's table, read and written through that library's own store, and forge no longer "+
		"projects an ORM store over a library's table. This project already generates one (%s), so it is kept for now; "+
		"it stops being generated in the next major version. Opt out now with a migration: "+
		"`COMMENT ON TABLE %s IS '%s'` (or say '%s' to keep the store past the next major).",
		t.Name, lt.shortLibrary(), file, tableRef(t), schemadef.TableMarkerNoEntity, schemadef.ViewMarkerEntity)
}
