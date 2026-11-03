package dashspec

import (
	"fmt"
	"regexp"

	"github.com/reliant-labs/forge/pkg/dbquery"
)

// PostgresQuery is what a KindPostgres panel draws: one read-only statement
// run live against a named database, and how its result set becomes a chart.
// The statement runs under dbquery.Run (read only, read committed, short
// statement and lock timeouts), so a dashboard never blocks other queries.
type PostgresQuery struct {
	// Database names the database to run against: the same name as `forge db
	// query` takes (a DNS-label-like identifier of at most 63 bytes).
	Database string `json:"database"`
	// SQL is one SELECT, WITH, TABLE or VALUES statement that may use the
	// PostgresParam* parameters. Nothing a viewer picks is spliced into it.
	SQL string `json:"sql"`
	// Draw is how the result is drawn: PostgresDrawAuto (or empty), line,
	// number, table or bar. Auto draws a line when Time names a column, a
	// number for one row and one value, a table otherwise.
	Draw string `json:"draw,omitempty"`
	// Time names the result column holding each row's timestamp. A line
	// requires it.
	Time string `json:"time,omitempty"`
	// Values names the numeric result columns to plot. Empty means every
	// column that is not Time or in Group. A number takes at most one.
	Values []string `json:"values,omitempty"`
	// Group names the result columns that split rows into separate series.
	Group []string `json:"group,omitempty"`
}

// The ways a PostgresQuery's result is drawn.
const (
	PostgresDrawAuto   = "auto"
	PostgresDrawLine   = "line"
	PostgresDrawNumber = "number"
	PostgresDrawTable  = "table"
	PostgresDrawBar    = "bar"
)

// The parameters a PostgresQuery's statement may use, as their positions:
// $1, $2 and $3. The values come from the dashboard's scope and are bound,
// never spliced. A statement need not reference any of them; one that
// references $2 must reference $1 too (Postgres cannot type a skipped one),
// and none may reference $4.
//
//	SELECT date_trunc('hour', created_at) AS t, count(*) AS signups
//	FROM users
//	WHERE created_at >= $1 AND created_at < $2
//	GROUP BY t ORDER BY t
const (
	// PostgresParamFrom is $1, the start of the dashboard's time range,
	// inclusive: a timestamptz.
	PostgresParamFrom = 1
	// PostgresParamTo is $2, the end of the range, exclusive: a timestamptz.
	PostgresParamTo = 2
	// PostgresParamStep is $3, a bucket width in seconds suited to the range,
	// for a time series: an int4.
	PostgresParamStep = 3
	// PostgresParamCount is how many parameters there are.
	PostgresParamCount = 3
)

// Bounds of a PostgresQuery.
const (
	// MaxPostgresDatabaseLen is the longest Database name.
	MaxPostgresDatabaseLen = 63
	// MaxPostgresColumnLen is the longest result column name (Postgres' own
	// identifier limit).
	MaxPostgresColumnLen = 63

	maxPostgresValues = maxSeriesPerPanel
	maxPostgresGroup  = maxGroupByPerPanel
)

// postgresDatabasePattern is a lowercase DNS-label-like name that may also
// contain underscores.
var postgresDatabasePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9_]*[a-z0-9])?$`)

// CheckPostgresSQL reports whether sql is a statement a KindPostgres panel
// may carry: at most dbquery.MaxSQLBytes, one read-only statement by the
// dbquery guard, referencing no parameter past PostgresParamCount. Like
// CheckSQL it is the first line of defence, not the containment: the reader
// runs the statement through dbquery.Run as a role confined to what the
// viewer may read.
func CheckPostgresSQL(sql string) error {
	if _, err := dbquery.Check(sql); err != nil {
		return err
	}
	n, err := dbquery.ParamRefs(sql)
	if err != nil {
		return err
	}
	if n > PostgresParamCount {
		return fmt.Errorf("the statement references $%d; a postgres panel binds only $%d (from), $%d (to) and $%d (step)", n, PostgresParamFrom, PostgresParamTo, PostgresParamStep)
	}
	return nil
}

func isPostgresDraw(s string) bool {
	switch s {
	case "", PostgresDrawAuto, PostgresDrawLine, PostgresDrawNumber, PostgresDrawTable, PostgresDrawBar:
		return true
	}
	return false
}
