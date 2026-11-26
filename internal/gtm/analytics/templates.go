// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/gtm"
)

// The sql templates. Each is ONE read-only SELECT over the org's hosted
// product_events table, bound by the control plane like any sql panel
// ({from}, {to}, {env_ids}), the way forge's built-in product dashboards read
// it (forge pkg/obsbuiltin/fragments.go). Nothing from a query is spliced in
// except validated names, as escaped string literals, and the row limit, as
// an integer. Every column a template selects is an aggregate or the
// dimension it groups by (an event name, a day, a property value); none is a
// user, session, visitor or any other identifier.

// scope is the window and the environments: forge's product-analytics
// {scope}. The control plane binds {env_ids} to the environments the
// credential may read; a sql panel's statement is never narrowed for it, so
// the predicate is what keeps an environment-bound token inside its binding.
const scope = "Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}" +
	" AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))"

// Result columns. countColumn and groupsColumn are numeric (the response's
// columns and values); the rest come back in each row's group.
const (
	eventColumn  = "event"
	dayColumn    = "day"
	valueColumn  = "value"
	countColumn  = "events"
	groupsColumn = "n_groups"
)

// groupsExpr counts the groups before LIMIT, so a result knows it was cut
// without asking for more rows than the cap.
const groupsExpr = "count() OVER () AS " + groupsColumn

// buildSQL is the statement for one of the sql templates. q is validated.
func buildSQL(q gtm.AnalyticsQuery) string {
	limit := " LIMIT " + strconv.Itoa(q.Limit)
	switch q.Template {
	case gtm.AnalyticsEventCounts:
		where := scope
		if len(q.Events) > 0 {
			where += " AND EventName IN (" + lits(q.Events) + ")"
		}
		return "SELECT EventName AS " + eventColumn + ", count() AS " + countColumn + ", " + groupsExpr +
			"\nFROM product_events\nWHERE " + where +
			"\nGROUP BY " + eventColumn +
			"\nORDER BY " + countColumn + " DESC, " + eventColumn + limit
	case gtm.AnalyticsEventTrend:
		return "SELECT toString(toDate(Timestamp, 'UTC')) AS " + dayColumn + ", EventName AS " + eventColumn + ", count() AS " + countColumn + ", " + groupsExpr +
			"\nFROM product_events\nWHERE " + scope + " AND EventName IN (" + lits(q.Events) + ")" +
			"\nGROUP BY " + dayColumn + ", " + eventColumn +
			"\nORDER BY " + dayColumn + ", " + eventColumn + limit
	case gtm.AnalyticsPropertyBreakdown:
		return "SELECT Props[" + lit(q.Property) + "] AS " + valueColumn + ", count() AS " + countColumn + ", " + groupsExpr +
			"\nFROM product_events\nWHERE " + scope + " AND EventName = " + lit(q.Events[0]) +
			"\nGROUP BY " + valueColumn +
			"\nORDER BY " + countColumn + " DESC, " + valueColumn + limit
	}
	return ""
}

// lit is s as a ClickHouse string literal. Validation already keeps quotes
// and backslashes out of every name; lit escapes them anyway.
func lit(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// lits is ss as a comma-separated list of string literals.
func lits(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = lit(s)
	}
	return strings.Join(q, ", ")
}
