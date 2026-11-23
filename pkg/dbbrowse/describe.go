package dbbrowse

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/dbquery"
)

// DescribeTimeout is the statement timeout [Describe]'s catalog queries run
// under.
const DescribeTimeout = 10 * time.Second

// Database is what one role can read in one database.
type Database struct {
	// Tables are sorted by schema, then name.
	Tables []Table `json:"tables"`
}

// TableKind is the kind of relation a [Table] is.
type TableKind string

const (
	KindTable            TableKind = "table"
	KindPartitionedTable TableKind = "partitioned_table"
	KindView             TableKind = "view"
	KindMaterializedView TableKind = "materialized_view"
	KindForeignTable     TableKind = "foreign_table"
)

// Table is one relation the role can read at least one column of. Partitions
// are not listed; their partitioned parent is.
type Table struct {
	Schema  string    `json:"schema"`
	Name    string    `json:"name"`
	Kind    TableKind `json:"kind"`
	Comment string    `json:"comment,omitempty"`
	// Columns are the readable columns, in ordinal order. A column the role
	// may not SELECT (a forge:secret column) is absent.
	Columns []Column `json:"columns"`
	// PrimaryKey names the key's columns in key order, nil when the table has
	// none (always, for a view) or when the role may not read all of them.
	PrimaryKey []string `json:"primary_key,omitempty"`
	// ForeignKeys are this table's references to other tables; ReferencedBy
	// are the references other tables make to this one (a row page's
	// "Related" section). Each is listed only when the role can follow it:
	// both tables are in the description and every column on both sides is
	// readable.
	ForeignKeys  []ForeignKey `json:"foreign_keys,omitempty"`
	ReferencedBy []ForeignKey `json:"referenced_by,omitempty"`
	// EstimatedRows is the planner's row estimate (pg_class.reltuples), -1
	// when the server has none: a table never vacuumed or analyzed, a view,
	// a partitioned parent.
	EstimatedRows int64 `json:"estimated_rows"`

	byName map[string]int
}

// Column is one readable column.
type Column struct {
	Name string `json:"name"`
	// Type is the column's type as Postgres prints it (format_type):
	// "uuid", "timestamp with time zone", "character varying(40)", "text[]",
	// an enum's name. It is for display; the compiler never writes it into
	// SQL.
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Comment  string `json:"comment,omitempty"`
	// Ordinal is the column's attribute number (pg_attribute.attnum): 1-based,
	// with gaps where columns were dropped.
	Ordinal int `json:"ordinal"`
	// Sortable reports whether the type has a default btree operator class,
	// so it can be ordered and compared with <, <=, >, >=. Enums, ranges,
	// records and arrays of sortable types are; json, xml, point and the other
	// geometric types are not.
	Sortable bool `json:"sortable"`
}

// ForeignKey is one foreign-key constraint, the same value in the
// referencing table's ForeignKeys and the referenced table's ReferencedBy.
// A composite key is one ForeignKey: Columns[i] references RefColumns[i].
type ForeignKey struct {
	Name string `json:"name"`
	// Schema, Table and Columns are the referencing side.
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	// RefSchema, RefTable and RefColumns are the referenced side.
	RefSchema  string   `json:"ref_schema"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
	// OnDelete is the referential action: "NO ACTION", "RESTRICT",
	// "CASCADE", "SET NULL" or "SET DEFAULT".
	OnDelete string `json:"on_delete"`
}

// QualifiedName is schema.name, unquoted: the form a [Request] names a table
// by.
func (t *Table) QualifiedName() string { return t.Schema + "." + t.Name }

// Column returns the readable column called name (exact, case-sensitive).
func (t *Table) Column(name string) (Column, bool) {
	if t.byName == nil {
		// A Table that did not come from Describe (decoded, hand-built):
		// scan rather than build the index, so a shared value is never
		// written to.
		for _, c := range t.Columns {
			if c.Name == name {
				return c, true
			}
		}
		return Column{}, false
	}
	i, ok := t.byName[name]
	if !ok {
		return Column{}, false
	}
	return t.Columns[i], true
}

func (t *Table) index() {
	t.byName = make(map[string]int, len(t.Columns))
	for i, c := range t.Columns {
		t.byName[c.Name] = i
	}
}

// Table returns the table whose [Table.QualifiedName] is name, or an error
// wrapping [ErrUnknownTable].
func (d *Database) Table(name string) (*Table, error) {
	var found *Table
	for i := range d.Tables {
		if d.Tables[i].QualifiedName() != name {
			continue
		}
		if found != nil {
			// "a.b"."c" and "a"."b.c": the dotted form cannot say which.
			return nil, fmt.Errorf("%w: %q names more than one table", ErrUnknownTable, name)
		}
		found = &d.Tables[i]
	}
	if found == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTable, name)
	}
	return found, nil
}

// bookkeeping are forge's own tables, which a browser has no reason to show:
// the migration ledgers (pkg/migratekit: golang-migrate's schema_migrations
// plus forge's schema_migrations_applied and schema_migrations_compat, and
// goose's table for adopted projects) and cronkit's tick claims. Matched by
// name in any schema, because each lands in whatever schema leads the
// migrating connection's search_path.
var bookkeeping = []string{
	"schema_migrations",
	"schema_migrations_applied",
	"schema_migrations_compat",
	"goose_db_version",
	"forge_cron_ticks",
}

// columnsQuery lists every column current_user may SELECT, with its
// relation. A relation none of whose columns is readable produces no row, so
// it is absent. has_column_privilege is true for a table-level grant too.
//
// sortable is the btree question asked of the column's type: its base type
// for a domain, its element type for an array (an array orders by its
// elements), and the polymorphic opclasses for enums, ranges, multiranges and
// composites. A binary-coercible cast covers types that borrow another's
// opclass (varchar uses text's). to_regtype rather than a ::regtype literal,
// so a server without multiranges (before 14) reads NULL instead of failing.
const columnsQuery = `
SELECT n.nspname::text, c.relname::text, c.relkind::text,
       CASE WHEN c.relkind IN ('r', 'm', 'f') AND c.reltuples >= 0 THEN c.reltuples::int8 ELSE -1 END,
       coalesce(pg_catalog.obj_description(c.oid, 'pg_class'), ''),
       a.attname::text, a.attnum::int4, pg_catalog.format_type(a.atttypid, a.atttypmod),
       NOT a.attnotnull,
       coalesce(pg_catalog.col_description(c.oid, a.attnum), ''),
       EXISTS (
         SELECT 1 FROM pg_catalog.pg_opclass oc JOIN pg_catalog.pg_am am ON am.oid = oc.opcmethod
          WHERE am.amname = 'btree' AND oc.opcdefault AND (
                oc.opcintype = et.oid
             OR (et.typtype = 'e' AND oc.opcintype = pg_catalog.to_regtype('pg_catalog.anyenum'))
             OR (et.typtype = 'r' AND oc.opcintype = pg_catalog.to_regtype('pg_catalog.anyrange'))
             OR (et.typtype = 'm' AND oc.opcintype = pg_catalog.to_regtype('pg_catalog.anymultirange'))
             OR (et.typtype = 'c' AND oc.opcintype = pg_catalog.to_regtype('pg_catalog.record'))
             OR EXISTS (SELECT 1 FROM pg_catalog.pg_cast k
                         WHERE k.castsource = et.oid AND k.casttarget = oc.opcintype AND k.castmethod = 'b')))
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
  JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
  JOIN pg_catalog.pg_type bt ON bt.oid = CASE WHEN t.typtype = 'd' THEN t.typbasetype ELSE t.oid END
  JOIN pg_catalog.pg_type et0 ON et0.oid = CASE WHEN bt.typcategory = 'A' AND bt.typelem <> 0 THEN bt.typelem ELSE bt.oid END
  JOIN pg_catalog.pg_type et ON et.oid = CASE WHEN et0.typtype = 'd' THEN et0.typbasetype ELSE et0.oid END
 WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
   AND NOT c.relispartition
   AND n.nspname NOT IN ('pg_catalog', 'information_schema')
   AND n.nspname !~ '^pg_toast'
   AND n.nspname !~ '^pg_temp_'
   AND NOT (c.relname = ANY ($1::text[]))
   AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
   AND pg_catalog.has_column_privilege(c.oid, a.attnum, 'SELECT')
 ORDER BY n.nspname::text COLLATE "C", c.relname::text COLLATE "C", a.attnum`

// keysQuery lists every primary key and foreign key of a user relation, each
// key's columns in key order. conparentid = 0 keeps a partitioned table's
// constraint and drops the copies Postgres makes on each partition. A key
// leaves the server only when the role has USAGE on the schema and SELECT on
// some column of every relation it names (the referencing relation and, for a
// foreign key, the referenced one), so a relation the role cannot inspect is
// never named in the result. Both sides are then filtered against the
// readable columns in Go, where the readable set is already in hand.
const keysQuery = `
SELECT con.contype::text, con.conname::text, n.nspname::text, c.relname::text,
       coalesce(rn.nspname::text, ''), coalesce(rc.relname::text, ''),
       ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
               JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
              ORDER BY k.ord),
       ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
               JOIN pg_catalog.pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
              ORDER BY k.ord),
       con.confdeltype::text
  FROM pg_catalog.pg_constraint con
  JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_catalog.pg_class rc ON rc.oid = con.confrelid
  LEFT JOIN pg_catalog.pg_namespace rn ON rn.oid = rc.relnamespace
 WHERE con.contype IN ('p', 'f')
   AND con.conparentid = 0
   AND n.nspname NOT IN ('pg_catalog', 'information_schema')
   AND n.nspname !~ '^pg_toast'
   AND n.nspname !~ '^pg_temp_'
   AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
   AND pg_catalog.has_any_column_privilege(c.oid, 'SELECT')
   AND (con.contype = 'p' OR (
        pg_catalog.has_schema_privilege(rn.oid, 'USAGE')
    AND pg_catalog.has_any_column_privilege(rc.oid, 'SELECT')))
 ORDER BY n.nspname::text COLLATE "C", c.relname::text COLLATE "C", con.conname::text COLLATE "C"`

var relkinds = map[string]TableKind{
	"r": KindTable,
	"p": KindPartitionedTable,
	"v": KindView,
	"m": KindMaterializedView,
	"f": KindForeignTable,
}

var onDelete = map[string]string{
	"a": "NO ACTION",
	"r": "RESTRICT",
	"c": "CASCADE",
	"n": "SET NULL",
	"d": "SET DEFAULT",
}

// Describe reports what the connected role can read: every table and view
// of every schema it has USAGE on, with the columns it may SELECT, primary
// keys, and foreign keys in both directions. Connect as the role the browse
// queries will run as (forge_ro): the description is that role's, and a
// table described for another role is an allow-list for the wrong reader.
//
// It reads only the catalogs, in a READ ONLY transaction under
// [DescribeTimeout], and changes nothing. A failure is a *[dbquery.Error],
// classified as [dbquery.Run]'s are (a timeout, an unreachable database, a
// privilege error), so one switch on its Kind serves Describe and Run alike.
func Describe(ctx context.Context, db dbquery.Beginner) (*Database, error) {
	ctx, cancel := context.WithTimeout(ctx, DescribeTimeout+2*time.Second)
	defer cancel()
	// REPEATABLE READ: the two catalog queries must see one snapshot, or a
	// key could name a column the first query never listed.
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, describeErr(err)
	}
	defer func() {
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rcancel()
		_ = tx.Rollback(rctx)
	}()
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', $1, true)`,
		strconv.FormatInt(DescribeTimeout.Milliseconds(), 10)+"ms"); err != nil {
		return nil, describeErr(err)
	}

	d := &Database{Tables: []Table{}}
	tables := map[[2]string]int{}
	rows, err := tx.Query(ctx, columnsQuery, bookkeeping)
	if err != nil {
		return nil, describeErr(err)
	}
	for rows.Next() {
		var (
			schema, name, kind, comment string
			est                         int64
			col                         Column
		)
		if err := rows.Scan(&schema, &name, &kind, &est, &comment,
			&col.Name, &col.Ordinal, &col.Type, &col.Nullable, &col.Comment, &col.Sortable); err != nil {
			rows.Close()
			return nil, describeErr(err)
		}
		key := [2]string{schema, name}
		i, ok := tables[key]
		if !ok {
			i = len(d.Tables)
			tables[key] = i
			d.Tables = append(d.Tables, Table{Schema: schema, Name: name, Kind: relkinds[kind], Comment: comment, EstimatedRows: est})
		}
		d.Tables[i].Columns = append(d.Tables[i].Columns, col)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, describeErr(err)
	}
	for i := range d.Tables {
		d.Tables[i].index()
	}

	// readable reports whether every one of cols is a readable column of the
	// described table at key.
	readable := func(key [2]string, cols []string) (int, bool) {
		i, ok := tables[key]
		if !ok || len(cols) == 0 {
			return 0, false
		}
		for _, c := range cols {
			if _, ok := d.Tables[i].byName[c]; !ok {
				return 0, false
			}
		}
		return i, true
	}

	rows, err = tx.Query(ctx, keysQuery)
	if err != nil {
		return nil, describeErr(err)
	}
	for rows.Next() {
		var (
			contype, del string
			fk           ForeignKey
		)
		if err := rows.Scan(&contype, &fk.Name, &fk.Schema, &fk.Table, &fk.RefSchema, &fk.RefTable,
			&fk.Columns, &fk.RefColumns, &del); err != nil {
			rows.Close()
			return nil, describeErr(err)
		}
		from, ok := readable([2]string{fk.Schema, fk.Table}, fk.Columns)
		if !ok {
			continue
		}
		if contype == "p" {
			d.Tables[from].PrimaryKey = fk.Columns
			continue
		}
		to, ok := readable([2]string{fk.RefSchema, fk.RefTable}, fk.RefColumns)
		if !ok || len(fk.Columns) != len(fk.RefColumns) {
			continue
		}
		fk.OnDelete = onDelete[del]
		d.Tables[from].ForeignKeys = append(d.Tables[from].ForeignKeys, fk)
		d.Tables[to].ReferencedBy = append(d.Tables[to].ReferencedBy, fk)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, describeErr(err)
	}
	for i := range d.Tables {
		refs := d.Tables[i].ReferencedBy
		sort.SliceStable(refs, func(a, b int) bool {
			if refs[a].Schema != refs[b].Schema {
				return refs[a].Schema < refs[b].Schema
			}
			if refs[a].Table != refs[b].Table {
				return refs[a].Table < refs[b].Table
			}
			return refs[a].Name < refs[b].Name
		})
	}
	return d, nil
}

// describeErr is a Describe failure as a *dbquery.Error, its Message saying
// what was being read. A new *Error wrapping err: one already in err's chain
// lends its Kind and is not written to.
func describeErr(err error) error {
	c := dbquery.Classify(err)
	return &dbquery.Error{Kind: c.Kind, Reason: c.Reason, Message: "reading the database's tables: " + c.Error(), Err: err}
}
