package obsdash

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// ProductUsageID is the id of the dashboard BuildProductUsage returns, so its
// file is "<project>-usage_gen.json". Deliberately not "product-usage": that is
// the id a project's own hand-written dashboard is likely to have (Reliant's
// is), and the console keys a dashboard on its id.
const ProductUsageID = "usage"

// retentionWeeks is how many weekly cohorts the retention panel draws: twelve
// weeks fit inside product_events' default 90-day retention (and the funnel
// read limit), so the oldest cohort is whole.
const retentionWeeks = 12

// firstSeenDays bounds how far back a user's first event is looked for: the
// new-vs-returning and retention panels define "first seen" as the first event
// in the firstSeenDays before the end of the range, never the oldest row the
// table still holds. product_events has finite retention (product_events_days,
// default 90), so "the oldest row" moves as rows expire and would turn a
// long-time user into a new one, and rebase their cohort, on whatever day their
// earlier events aged out. A fixed window gives the same answer under any
// retention of at least firstSeenDays: a user idle for longer than the window
// counts as new (reactivated) on their return. It equals the default
// retention, so a default environment holds the whole window.
const firstSeenDays = 90

// firstSeenWindow is the window retention's first-seen reads: the
// firstSeenDays before the end of the range, whatever range is selected (the
// cohorts are the retentionWeeks before its end, inside the window).
var firstSeenWindow = fmt.Sprintf("Timestamp >= {to:DateTime64(9)} - INTERVAL %d DAY AND Timestamp < {to:DateTime64(9)}", firstSeenDays)

// firstSeenScope is new-vs-returning's first-seen window: firstSeenWindow,
// reaching back to the start of the range when that is older (a range longer
// than the window), so every day's active users have a first day.
var firstSeenScope = fmt.Sprintf("Timestamp >= least({from:DateTime64(9)}, {to:DateTime64(9)} - INTERVAL %d DAY)"+
	" AND Timestamp < {to:DateTime64(9)}", firstSeenDays)

// productScope restricts a product_events query to the dashboard's range and
// its environment and service filters. EnvId is the event's reliant.env.id, so
// the environment scope is {env_ids}.
const productScope = "Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}" +
	" AND " + productFilterScope

// productFilterScope is productScope without the range, for the panels that
// read further back than the selected range (first-seen, retention).
const productFilterScope = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))" +
	" AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))"

// crudEvent matches the automatic CRUD product events (pkg/crud): the entity
// is the name with the verb suffix removed, so an entity added later needs no
// regenerate to appear.
const (
	crudEventRE = `'_(created|updated|deleted)$'`
	crudEntity  = "replaceRegexpOne(EventName, " + crudEventRE + ", '')"
)

// BuildProductUsage is the "Product usage" dashboard: who uses the product and
// what they do, over the hosted product_events table. Every panel is a sql
// panel, which the Reliant console runs against the environment's own data;
// the local ClickStack has no product_events table, so like Workloads and
// Releases it ships in the spec and the release bundle only.
//
// It reads two kinds of event without the project naming any: the automatic
// <entity>_created|_updated|_deleted forge's generated CRUD handlers emit
// (pkg/crud), and the browser's page_viewed (prop "route"). Every other event
// the app tracks shows up in the "Events by name" table. Users are UserId, the
// authenticated subject; anonymous events count toward events, not users.
func BuildProductUsage(project string) Dashboard {
	d := Dashboard{
		ID:    ProductUsageID,
		Title: project + ": product usage",
		Description: "Who uses the product and what they do, from product events: active users, new against returning, " +
			"weekly retention, the pages people view and the records they create, update and delete. " +
			"CRUD events are emitted automatically by the generated handlers (signals.product_events = False turns them off); " +
			"page_viewed comes from the browser runtime. Hosted environments only: a local forge env up has no product_events table.",
		Group:   GroupProduct,
		Filters: commonFilters(),
		// Daily buckets and a 30-day MAU: an hour would draw one point.
		DefaultRange: "30d",
	}
	daily := "1d"
	stacked := &dashspec.Display{Style: dashspec.StyleBars, Stack: true}
	d.Panels = []Panel{
		{ID: "active-users", Title: "Active users",
			Description: "Distinct signed-in users with any product event in the day, 7 days and 30 days before the end of the range " +
				"(DAU, WAU, MAU), and stickiness: DAU over MAU.",
			Kind: KindSQL, SQL: activeUsersSQL, Width: 24, Height: 4},
		{ID: "daily-active-users", Title: "Daily active users",
			Description: "Distinct signed-in users with any product event, per UTC day.",
			Kind:        KindSQL, SQL: dailyActiveUsersSQL, MinStep: daily, Width: 12, Height: 8},
		{ID: "new-vs-returning", Title: "New vs returning users per day",
			Description: fmt.Sprintf("Each day's active users in exactly one class: new on the day of their first event, returning after it. "+
				"First-seen is the first event in the %d days before the end of the range (or since the start of a longer range), "+
				"so a user idle for longer than that counts as new again on their return.", firstSeenDays),
			Kind: KindSQL, SQL: newVsReturningSQL, MinStep: daily, Display: stacked, Width: 12, Height: 8},
		{ID: "weekly-retention", Title: "Weekly retention by cohort",
			Description: fmt.Sprintf("Users grouped by the week (Monday, UTC) of their first event, over the %d weeks before the end of the range "+
				"(whatever range is selected), and the percentage active again N weeks later. A week that has not happened yet is empty. "+
				"A user's first event is their first in the %d days before the end of the range, so one idle for longer joins the cohort of their return.",
				retentionWeeks, firstSeenDays),
			Kind: KindSQL, SQL: weeklyRetentionSQL(), Width: 24, Height: 9},
		{ID: "top-pages", Title: "Top pages",
			Description: "page_viewed events by route: views, distinct signed-in users and sessions.",
			Kind:        KindSQL, SQL: topPagesSQL, Width: 12, Height: 8},
		{ID: "entity-actions", Title: "Records created, updated and deleted",
			Description: "The automatic CRUD product events, per entity and action, over time.",
			Kind:        KindSQL, SQL: entityActionsSQL, Display: stacked, Width: 12, Height: 8},
		{ID: "entity-actions-by-entity", Title: "Actions per entity",
			Description: "Creates, updates and deletes per entity in the range, and the distinct users who made them.",
			Kind:        KindSQL, SQL: entityTotalsSQL, Width: 12, Height: 8},
		{ID: "most-active-users", Title: "Most active users",
			Description: "Signed-in users by product events in the range, with the days they were active and when they were last seen.",
			Kind:        KindSQL, SQL: mostActiveUsersSQL, Width: 12, Height: 8},
		{ID: "events-by-name", Title: "Events by name",
			Description: "Every product event in the range, automatic or tracked by the app, with the distinct users who did it.",
			Kind:        KindSQL, SQL: eventsByNameSQL, Width: 24, Height: 8},
	}
	return d
}

const activeUsersSQL = `SELECT uniqExactIf(UserId, Timestamp >= {to:DateTime64(9)} - INTERVAL 1 DAY) AS dau,
       uniqExactIf(UserId, Timestamp >= {to:DateTime64(9)} - INTERVAL 7 DAY) AS wau,
       uniqExact(UserId) AS mau,
       round(dau / nullIf(mau, 0), 2) AS stickiness
FROM product_events
WHERE UserId != '' AND Timestamp >= {to:DateTime64(9)} - INTERVAL 30 DAY AND Timestamp < {to:DateTime64(9)}
  AND ` + productFilterScope

const dailyActiveUsersSQL = `SELECT toStartOfDay(Timestamp) AS day, uniqExact(UserId) AS users
FROM product_events
WHERE UserId != '' AND ` + productScope + `
GROUP BY day ORDER BY day`

var newVsReturningSQL = `SELECT a.day AS day,
       countIf(a.day = f.first_day) AS new_users,
       countIf(a.day > f.first_day) AS returning_users
FROM (
    SELECT toStartOfDay(Timestamp) AS day, UserId
    FROM product_events
    WHERE UserId != '' AND ` + productScope + `
    GROUP BY day, UserId
) AS a
INNER JOIN (
    SELECT UserId, toStartOfDay(min(Timestamp)) AS first_day
    FROM product_events
    WHERE UserId != '' AND ` + firstSeenScope + ` AND ` + productFilterScope + `
    GROUP BY UserId
) AS f ON a.UserId = f.UserId
GROUP BY day ORDER BY day`

// weeklyRetentionSQL is one row per weekly cohort (the Monday of a user's first
// event), with the cohort's size and, for each later week, the percentage of
// the cohort active in it. A week after the end of the range is NULL rather
// than 0, so a young cohort does not read as churned.
func weeklyRetentionSQL() string {
	var weeks strings.Builder
	for w := 1; w < retentionWeeks; w++ {
		fmt.Fprintf(&weeks, ",\n       if(cohort + INTERVAL %d WEEK <= toMonday({to:DateTime64(9)}), round(100 * countIf(has(weeks, cohort + INTERVAL %d WEEK)) / count(), 1), NULL) AS week_%d", w, w, w)
	}
	// The cohort is text so the rows read as a table, not as a time series.
	return `SELECT toString(cohort) AS cohort_week, count() AS users` + weeks.String() + `
FROM (
    SELECT UserId, min(week) AS cohort, groupUniqArray(week) AS weeks
    FROM (
        SELECT UserId, toMonday(Timestamp) AS week
        FROM product_events
        WHERE UserId != '' AND ` + firstSeenWindow + ` AND ` + productFilterScope + `
    )
    GROUP BY UserId
)
WHERE cohort >= toMonday({to:DateTime64(9)}) - INTERVAL ` + fmt.Sprint(retentionWeeks-1) + ` WEEK
GROUP BY cohort ORDER BY cohort`
}

const topPagesSQL = `SELECT Props['route'] AS route, count() AS views,
       uniqExactIf(UserId, UserId != '') AS users,
       uniqExactIf(SessionId, SessionId != '') AS sessions
FROM product_events
WHERE EventName = 'page_viewed' AND ` + productScope + `
GROUP BY route ORDER BY views DESC LIMIT 25`

const entityActionsSQL = `SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t,
       concat(` + crudEntity + `, ' ', extract(EventName, '(created|updated|deleted)$')) AS action,
       count() AS events
FROM product_events
WHERE match(EventName, ` + crudEventRE + `) AND ` + productScope + `
GROUP BY t, action ORDER BY t`

const entityTotalsSQL = `SELECT ` + crudEntity + ` AS entity,
       countIf(endsWith(EventName, '_created')) AS created,
       countIf(endsWith(EventName, '_updated')) AS updated,
       countIf(endsWith(EventName, '_deleted')) AS deleted,
       uniqExactIf(UserId, UserId != '') AS users
FROM product_events
WHERE match(EventName, ` + crudEventRE + `) AND ` + productScope + `
GROUP BY entity ORDER BY created + updated + deleted DESC LIMIT 50`

const mostActiveUsersSQL = `SELECT UserId AS user_id, count() AS events,
       uniqExact(toDate(Timestamp)) AS active_days,
       uniqExact(EventName) AS distinct_events,
       formatDateTime(max(Timestamp), '%Y-%m-%d %H:%i') AS last_seen_utc
FROM product_events
WHERE UserId != '' AND ` + productScope + `
GROUP BY UserId ORDER BY events DESC, user_id LIMIT 25`

const eventsByNameSQL = `SELECT EventName AS event, count() AS events, uniqExactIf(UserId, UserId != '') AS users
FROM product_events
WHERE ` + productScope + `
GROUP BY EventName ORDER BY events DESC LIMIT 100`
