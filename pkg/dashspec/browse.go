package dashspec

import "regexp"

// PanelLink makes each row of a panel open the data browser: a table or bar
// panel's row, or a postgres panel's drawn as a table or bar (draw table, bar
// or auto). Only the Reliant console follows it; HyperDX ignores it.
//
// A link takes one of two forms. With Column and FromField it opens Table
// filtered to the rows whose Column equals the row's FromField value:
//
//	"link": {"database": "app", "table": "public.users", "column": "id", "fromField": "span.attr.user_id"}
//
// With Row and FromFields it opens one row of Table, its primary key read from
// the row's FromFields in key order:
//
//	"link": {"database": "app", "table": "public.orders", "row": true, "fromFields": ["order_id"]}
//
// A FromField of a table or bar panel is one of the panel's GroupBy fields,
// the only values its rows carry. A postgres panel's is a result column, which
// the spec cannot see: a row whose result has no such column is drawn
// unlinked, never as an error. That Database and Table exist is the console's
// question (it asks the database itself); a link to one that does not is drawn
// disabled.
//
// A telemetry panel may link into Postgres: a bar of the top
// span.attr.user_id values linking to the users table by id is the main use.
type PanelLink struct {
	// Database is a database name, as PostgresQuery.Database.
	Database string `json:"database"`
	// Table is schema-qualified: "public.users".
	Table string `json:"table"`
	// Column is the column of Table to filter on, FromField the field of the
	// panel's row holding the value.
	Column    string `json:"column,omitempty"`
	FromField string `json:"fromField,omitempty"`
	// Row opens one row of Table; FromFields name its primary key's values
	// in key order.
	Row        bool     `json:"row,omitempty"`
	FromFields []string `json:"fromFields,omitempty"`
}

// MaxLinkKeyFields bounds PanelLink.FromFields: the widest primary key a row
// link reads.
const MaxLinkKeyFields = 8

// MaxTableNameLen is the longest schema-qualified table name: two Postgres
// identifiers and the dot between them.
const MaxTableNameLen = 2*MaxPostgresColumnLen + 1

// KindBrowse's request: one table of one database, as the data browser reads
// it. The console runs it through the data browser's own read (the same
// admission and audit); nothing in it is SQL, so it stays inside the columns
// the read-only role may read, and opening it in the data browser ("Open in
// Data") is the same request.
//
//	"browse": {"database": "app", "table": "public.users",
//	  "columns": ["id", "email", "created_at"],
//	  "filters": [{"column": "created_at", "op": "gte", "values": ["{from}"]},
//	              {"column": "plan", "op": "eq", "values": ["pro"]}],
//	  "sort": [{"column": "created_at", "desc": true}],
//	  "size": 50}
type BrowseView struct {
	// Database is a database name, as PostgresQuery.Database.
	Database string `json:"database"`
	// Table is schema-qualified: "public.users".
	Table string `json:"table"`
	// Columns are the columns shown, in order; empty shows every column the
	// role may read.
	Columns []string `json:"columns,omitempty"`
	// Filters are ANDed.
	Filters []BrowseFilter `json:"filters,omitempty"`
	// Sort keys, most significant first. The data browser adds the primary
	// key after them, so a page boundary is stable.
	Sort []BrowseSort `json:"sort,omitempty"`
	// Size is the page size: one of BrowseSizes, zero for DefaultBrowseSize.
	Size int `json:"size,omitempty"`
}

// BrowseFilter is one condition of a BrowseView.
//
// Values holds one value for the comparison and like operators, none for
// is_null and not_null, and 1 to MaxBrowseInValues for in (see
// BrowseOpArity). A value is text in the form Postgres reads for the column's
// type. A value that is exactly a parameter reference, `{name}`, is the
// dashboard's parameter instead (see BrowseParam); it is bound as a value,
// never spliced into anything.
type BrowseFilter struct {
	Column string   `json:"column"`
	Op     BrowseOp `json:"op"`
	Values []string `json:"values,omitempty"`
}

// BrowseSort is one sort key of a BrowseView.
type BrowseSort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

// BrowseOp is a BrowseFilter operator. The set is the data browser's
// (dbbrowse.Op), duplicated here because the spec must not depend on the
// browser's database code: keep the two equal, and pin them with a test
// wherever both are imported.
type BrowseOp string

// The filter operators.
const (
	BrowseOpEq  BrowseOp = "eq"
	BrowseOpNeq BrowseOp = "neq"
	BrowseOpLt  BrowseOp = "lt"
	BrowseOpLte BrowseOp = "lte"
	BrowseOpGt  BrowseOp = "gt"
	BrowseOpGte BrowseOp = "gte"
	// BrowseOpLike and BrowseOpIlike match the column's text form against a
	// LIKE pattern (% and _ wildcards, backslash escapes).
	BrowseOpLike    BrowseOp = "like"
	BrowseOpIlike   BrowseOp = "ilike"
	BrowseOpIsNull  BrowseOp = "is_null"
	BrowseOpNotNull BrowseOp = "not_null"
	// BrowseOpIn matches any of 1 to MaxBrowseInValues values.
	BrowseOpIn BrowseOp = "in"
)

// BrowseOps returns every filter operator, in the order the data browser
// lists them.
func BrowseOps() []BrowseOp {
	return []BrowseOp{BrowseOpEq, BrowseOpNeq, BrowseOpLt, BrowseOpLte, BrowseOpGt, BrowseOpGte,
		BrowseOpLike, BrowseOpIlike, BrowseOpIsNull, BrowseOpNotNull, BrowseOpIn}
}

// BrowseOpArity is how many values op takes, -1 for in (1 to
// MaxBrowseInValues), and false for an operator that is not one.
func BrowseOpArity(op BrowseOp) (n int, ok bool) {
	switch op {
	case BrowseOpEq, BrowseOpNeq, BrowseOpLt, BrowseOpLte, BrowseOpGt, BrowseOpGte, BrowseOpLike, BrowseOpIlike:
		return 1, true
	case BrowseOpIsNull, BrowseOpNotNull:
		return 0, true
	case BrowseOpIn:
		return -1, true
	}
	return 0, false
}

// Bounds of a BrowseView, the data browser's own where it has one, so a view
// that validates here is a request it serves.
const (
	// MaxBrowseFilters bounds BrowseView.Filters, MaxBrowseInValues an in
	// filter's values, and MaxBrowseValueBytes one value's length.
	MaxBrowseFilters    = 32
	MaxBrowseInValues   = 100
	MaxBrowseValueBytes = 4 << 10
	// MaxBrowseColumns bounds BrowseView.Columns, MaxBrowseSort its sort keys.
	MaxBrowseColumns = 100
	MaxBrowseSort    = 8
	// DefaultBrowseSize is the page size of a view that names none.
	DefaultBrowseSize = 50
)

// BrowseSizes returns the page sizes a BrowseView may name: the data
// browser's choices.
func BrowseSizes() []int { return []int{25, 50, 100, 200, 500} }

// The dashboard parameters a BrowseFilter value may reference besides the
// dashboard's own filters.
const (
	// BrowseParamFrom is `{from}`, the start of the dashboard's time range,
	// inclusive, as an RFC 3339 timestamp.
	BrowseParamFrom = "from"
	// BrowseParamTo is `{to}`, the end of the range, exclusive.
	BrowseParamTo = "to"
)

// browseParamPattern is a whole value that is a parameter reference. The name
// is a filter id's shape (idPattern), which also covers from and to.
var browseParamPattern = regexp.MustCompile(`^\{([A-Za-z0-9][A-Za-z0-9_.-]*)\}$`)

// BrowseParam reports whether a BrowseFilter value is a reference to a
// dashboard parameter, and names it. A reference is the whole value: `{from}`,
// `{to}`, or `{<id>}` of a filter the dashboard declares, which is that
// filter's value (a toggle's is "true" or "false"). from and to are always
// the time range, whatever the dashboard's filters are called. A filter whose
// value references a dashboard filter the viewer has not set is left out of
// the request, so an unset filter narrows nothing, as it does everywhere else.
// A value that only contains braces ("{a} or {b}") is a literal.
func BrowseParam(value string) (name string, ok bool) {
	m := browseParamPattern.FindStringSubmatch(value)
	if m == nil {
		return "", false
	}
	return m[1], true
}
