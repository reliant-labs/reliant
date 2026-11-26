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
// an integer. Every column a template returns is an aggregate or the
// dimension it groups by (an event name, a day, a property value); none is a
// user, session, visitor or any other identifier. UserId is read in exactly
// one expression, peopleExpr's uniqIf(UserId, UserId != ''), for the
// k-anonymity threshold.

// scope is the window and the environments: forge's product-analytics
// {scope}. The control plane binds {env_ids} to the environments the
// credential may read; a sql panel's statement is never narrowed for it, so
// the predicate is what keeps an environment-bound token inside its binding.
const scope = "Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}" +
	" AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))"

// MinGroupPeople is the k of event_counts' and property_breakdown's
// k-anonymity: a group (an event, a property value) is returned only when at
// least this many distinct signed-in users are in it. The rest are counted
// in AnalyticsResult.HiddenGroups, never listed.
const MinGroupPeople = 3

// peopleExpr is the one expression a template reads UserId in: the number of
// distinct signed-in users in a group, compared with MinGroupPeople (for the
// groups shown and the groups hidden alike) and never returned. Anonymous
// events (an empty UserId) count no one, so two users and any amount of
// anonymous traffic stay hidden.
const peopleExpr = "uniqIf(UserId, UserId != '') AS people"

// Result columns. countColumn, groupsColumn and hiddenColumn are numeric
// (the response's columns and values); the rest come back in each row's
// group.
const (
	kindColumn   = "kind"
	eventColumn  = "event"
	dayColumn    = "day"
	valueColumn  = "value"
	countColumn  = "events"
	groupsColumn = "n_groups"
	hiddenColumn = "hidden_groups"
)

// The kinds of row a k-anonymous template returns: the shown groups, then
// one summary row with the counts of shown and hidden groups.
const (
	kindGroup   = "group"
	kindSummary = "summary"
)

// buildSQL is the statement for one of the sql templates. q is validated.
func buildSQL(q gtm.AnalyticsQuery) string {
	switch q.Template {
	case gtm.AnalyticsEventCounts:
		where := scope
		if len(q.Events) > 0 {
			where += " AND EventName IN (" + lits(q.Events) + ")"
		}
		return kAnonymous("EventName", eventColumn, where, q.Limit)
	case gtm.AnalyticsEventTrend:
		// Newest days first under the LIMIT, so a cut drops the oldest;
		// decode puts them back in time order. count() OVER () counts the
		// groups before LIMIT, so the result knows it was cut.
		return "SELECT toString(toDate(Timestamp, 'UTC')) AS " + dayColumn + ", EventName AS " + eventColumn +
			", count() AS " + countColumn + ", count() OVER () AS " + groupsColumn +
			"\nFROM product_events\nWHERE " + scope + " AND EventName IN (" + lits(q.Events) + ")" +
			"\nGROUP BY " + dayColumn + ", " + eventColumn +
			"\nORDER BY " + dayColumn + " DESC, " + eventColumn +
			"\nLIMIT " + strconv.Itoa(q.Limit)
	case gtm.AnalyticsPropertyBreakdown:
		return kAnonymous("Props["+lit(q.Property)+"]", valueColumn, scope+" AND EventName = "+lit(q.Events[0]), q.Limit)
	}
	return ""
}

// kAnonymous is a top-limit count of events grouped by dim (as col), over
// the rows where holds, returning only the groups of at least MinGroupPeople
// users. One summary row follows the groups (kind 'summary') with the number
// of groups shown before the LIMIT and the number hidden by the threshold, so
// a caller can tell suppressed data from missing data even when every group
// is hidden.
func kAnonymous(dim, col, where string, limit int) string {
	k := strconv.Itoa(MinGroupPeople)
	return "WITH g AS (\n" +
		"  SELECT " + dim + " AS " + col + ", count() AS " + countColumn + ", " + peopleExpr + "\n" +
		"  FROM product_events\n" +
		"  WHERE " + where + "\n" +
		"  GROUP BY " + col + "\n" +
		")\n" +
		"SELECT " + kindColumn + ", " + col + ", " + countColumn + ", " + groupsColumn + ", " + hiddenColumn + " FROM (\n" +
		"  SELECT '" + kindGroup + "' AS " + kindColumn + ", " + col + ", " + countColumn + ", toUInt64(0) AS " + groupsColumn + ", toUInt64(0) AS " + hiddenColumn + "\n" +
		"  FROM (SELECT " + col + ", " + countColumn + " FROM g WHERE people >= " + k + " ORDER BY " + countColumn + " DESC, " + col + " LIMIT " + strconv.Itoa(limit) + ")\n" +
		"  UNION ALL\n" +
		"  SELECT '" + kindSummary + "' AS " + kindColumn + ", '' AS " + col + ", toUInt64(0) AS " + countColumn +
		", countIf(people >= " + k + ") AS " + groupsColumn + ", countIf(people < " + k + ") AS " + hiddenColumn + "\n" +
		"  FROM g\n" +
		")"
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
