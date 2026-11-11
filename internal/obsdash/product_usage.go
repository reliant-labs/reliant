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

// The sections of the Product usage dashboard, in the order its panels run.
// A console draws each as a heading over its panels, and lets the reader hide
// one.
var productUsageSections = []Section{
	{ID: "glance", Title: "At a glance",
		Description: "The headline figures, each beside its change against the previous period and its trend across the range."},
	{ID: "users", Title: "Users",
		Description: "Who is active, how many of them are new, and who uses the product most."},
	{ID: "activity", Title: "What users do",
		Description: "The records people write through the generated CRUD handlers, the pages they view and the events they send."},
	{ID: "retention", Title: "Retention",
		Description: "Whether people come back: each weekly cohort's share active again in the weeks after its first."},
}

// The colours the dashboard gives each thing it counts, so one concept reads
// as one colour on every panel: active users blue, the rolling windows cyan
// and violet, new users amber, events fuchsia, anonymous visitors slate (a
// neutral), and the CRUD actions three distinct hues (deleting is not an
// error, so not danger).
const (
	colorActive    = dashspec.ColorBlue
	colorWeekly    = dashspec.ColorCyan
	colorMonthly   = dashspec.ColorViolet
	colorNew       = dashspec.ColorAmber
	colorEvents    = dashspec.ColorFuchsia
	colorAnonymous = dashspec.ColorSlate
	colorCreated   = dashspec.ColorCyan
	colorUpdated   = dashspec.ColorViolet
	colorDeleted   = dashspec.ColorAmber
)

// signedInColors colours the charts that split signed-in against anonymous.
var signedInColors = dashspec.SeriesColors{"Signed in": colorActive, "Anonymous": colorAnonymous}

// BuildProductUsage is the "Product usage" dashboard: who uses the product and
// what they do, over the hosted product_events table. Every panel is a sql
// panel, which the Reliant console runs against the environment's own data;
// the local ClickStack has no product_events table, so like Workloads and
// Releases it ships in the spec and the release bundle only.
//
// It reads two kinds of event without the project naming any: the automatic
// <entity>_created|_updated|_deleted forge's generated CRUD handlers emit
// (pkg/crud), and the browser's page_viewed (prop "route"). Every other event
// the app tracks shows up in "Events by name". Users are UserId, the
// authenticated subject; anonymous events count toward events, not users.
//
// The layout is the console's four-column grid (a width of 6 is one column):
// two rows of four headline figures, then pairs of half-width charts, and the
// most-active-users table last. A chart's colours are the console's palette,
// one per series (or per value column of a bar chart), so the charts that
// should read as several colours return several value columns: signed-in
// against anonymous, created against updated against deleted.
func BuildProductUsage(project string) Dashboard {
	d := Dashboard{
		ID:    ProductUsageID,
		Title: project + ": product usage",
		Description: "Who uses the product and what they do, from product events: active users, new against returning, " +
			"weekly retention, the pages people view and the records they create, update and delete. " +
			"CRUD events are emitted automatically by the generated handlers (signals.product_events = False turns them off); " +
			"page_viewed comes from the browser runtime. Each headline figure opens beside its change against the previous period. " +
			"Hosted environments only: a local forge env up has no product_events table.",
		Group:    GroupProduct,
		Filters:  commonFilters(),
		Sections: productUsageSections,
		// Daily buckets and a 30-day MAU: an hour would draw one point.
		DefaultRange: "30d",
		// A dashboard of headline figures: each opens beside its change.
		DefaultCompare: dashspec.ComparePeriod,
	}
	const daily = "1d"
	stackedBars := func(colors dashspec.SeriesColors) *dashspec.Display {
		return &dashspec.Display{Style: dashspec.StyleBars, Stack: true, Colors: &colors}
	}

	// A headline figure: one row of one value, its trend across the range
	// drawn under it as a sparkline, in the colour of what it counts.
	figure := func(id, title, desc, sql, trend string, unit Unit, color dashspec.ChartColor) Panel {
		return Panel{Section: "glance", ID: id, Title: title, Description: desc, Kind: KindSQL, SQL: sql, TrendSQL: trend, Unit: unit,
			Display: &dashspec.Display{Color: color}, Width: 6, Height: 4}
	}
	chart := func(section, id, title, desc, sql string, display *dashspec.Display) Panel {
		return Panel{Section: section, ID: id, Title: title, Description: desc, Kind: KindSQL, SQL: sql, Display: display, Width: 12, Height: 9}
	}

	newVsReturning := chart("users", "new-vs-returning", "New vs returning users per day",
		fmt.Sprintf("Each day's active users in exactly one class: new on the day of their first event, returning after it. "+
			"First-seen is the first event in the %d days before the end of the range (or since the start of a longer range), "+
			"so a user idle for longer than that counts as new again on their return.", firstSeenDays),
		newVsReturningSQL, stackedBars(dashspec.SeriesColors{"New": colorNew, "Returning": colorActive}))
	newVsReturning.MinStep = daily
	activeOverTime := chart("users", "active-users-over-time", "Active users",
		"Distinct signed-in users per UTC day, and over the 7 days ending that day (the rolling weekly active users), drawn as areas.",
		activeUsersOverTimeSQL, &dashspec.Display{Style: dashspec.StyleArea,
			Colors: &dashspec.SeriesColors{"Weekly active (rolling 7 days)": colorWeekly, "Daily active": colorActive}})
	activeOverTime.MinStep = daily
	retentionCurve := chart("retention", "retention-curve", "Retention by cohort: weeks 1, 2, 4 and 8",
		fmt.Sprintf("For each weekly cohort (users by the Monday, UTC, of their first event, the %d weeks before the end of the range), "+
			"the share active again 1, 2, 4 and 8 weeks later. A week that has not happened yet is a gap, not a drop.", retentionWeeks),
		retentionCurveSQL(), &dashspec.Display{Colors: &dashspec.SeriesColors{
			"Week 1": dashspec.ColorBlue, "Week 2": dashspec.ColorCyan, "Week 4": dashspec.ColorViolet, "Week 8": dashspec.ColorFuchsia}})
	retentionCurve.Unit = UnitPercent
	retentionCurve.MinStep = "7d"
	retentionCurve.Width, retentionCurve.Height = 24, 8
	retentionGrid := chart("retention", "weekly-retention", "Weekly retention by cohort",
		fmt.Sprintf("One row per weekly cohort: users grouped by the week (Monday, UTC) of their first event, over the %d weeks before the end of the range "+
			"(whatever range is selected), with the cohort's size. Each cell is the share of the cohort active again N weeks later (W1 is the week after the cohort's), "+
			"darker for more. A week that has not happened yet is empty. A user's first event is their first in the %d days before the end of the range, "+
			"so one idle for longer joins the cohort of their return.", retentionWeeks, firstSeenDays),
		weeklyRetentionSQL(), &dashspec.Display{Style: dashspec.StyleHeatmap, Color: colorActive})
	retentionGrid.Unit = UnitPercent
	retentionGrid.Width, retentionGrid.Height = 24, 11
	// Entities are the project's own, so their colours are dealt by rank.
	entityTimeline := chart("activity", "records-by-entity", "Records written per entity",
		"The automatic CRUD product events (creates, updates and deletes together), one colour per entity, stacked: the top edge is every write.",
		recordsByEntitySQL, &dashspec.Display{Style: dashspec.StyleBars, Stack: true, TopN: 12})

	d.Panels = []Panel{
		figure("dau", "Daily active users", "Distinct signed-in users with any product event in the 24 hours before the end of the range; the trend is each UTC day's.",
			dauSQL, dauTrendSQL, UnitCount, colorActive),
		figure("wau", "Weekly active users", "Distinct signed-in users with any product event in the 7 days before the end of the range; the trend is the 7 days ending each UTC day.",
			wauSQL, rollingActiveTrendSQL(7, "Weekly active users"), UnitCount, colorWeekly),
		figure("mau", "Monthly active users", "Distinct signed-in users with any product event in the 30 days before the end of the range; the trend is the 30 days ending each UTC day.",
			mauSQL, rollingActiveTrendSQL(30, "Monthly active users"), UnitCount, colorMonthly),
		figure("stickiness", "Stickiness (DAU / MAU)", "The share of the month's active users who were active in the last day: how habitual the product is. The trend is each UTC day's.",
			stickinessSQL, stickinessTrendSQL, UnitPercent, colorMonthly),
		figure("new-users", "New users", fmt.Sprintf("Signed-in users whose first event (within the %d days before the end of the range) falls in the range; the trend is each UTC day's.", firstSeenDays),
			newUsersSQL, newUsersTrendSQL, UnitCount, colorNew),
		figure("events", "Events", "Every product event in the range, automatic or tracked by the app, signed in or anonymous.",
			eventsSQL, eventsTrendSQL, UnitCount, colorEvents),
		figure("events-per-user", "Events per active user", "Product events in the range over the signed-in users active in it; the trend is each UTC day's.",
			eventsPerUserSQL, eventsPerUserTrendSQL, "", colorEvents),
		figure("records-written", "Records written", "Creates, updates and deletes through the generated CRUD handlers in the range.",
			recordsWrittenSQL, recordsWrittenTrendSQL, UnitCount, colorCreated),

		activeOverTime,
		newVsReturning,
		{Section: "users", ID: "most-active-users", Title: "Most active users",
			Description: "Signed-in users by product events in the range, with the days they were active and when they were last seen.",
			Kind:        KindSQL, SQL: mostActiveUsersSQL, Width: 24, Height: 9},

		entityTimeline,
		chart("activity", "actions-per-entity", "Creates, updates and deletes per entity",
			"The CRUD mix of each entity in the range: one bar per entity, a colour per action.", actionsPerEntitySQL,
			stackedBars(dashspec.SeriesColors{"Created": colorCreated, "Updated": colorUpdated, "Deleted": colorDeleted})),
		chart("activity", "top-pages", "Top pages",
			"page_viewed events by route, signed-in views against anonymous ones.", topPagesSQL, stackedBars(signedInColors)),
		chart("activity", "events-by-name", "Events by name",
			"The most frequent product events in the range, automatic or tracked by the app, signed in against anonymous.", eventsByNameSQL, stackedBars(signedInColors)),

		retentionGrid,
		retentionCurve,
	}
	return d
}

// activeUserScope keeps the events of signed-in users in the range.
const activeUserScope = "UserId != '' AND " + productScope

// The headline figures. Each is one row of one numeric column, which a console
// draws as a figure; DAU, WAU and MAU are the windows ending at the end of the
// range, whatever its start, so they compare across ranges.
const (
	dauSQL = `SELECT uniqExact(UserId) AS "Daily active users"
FROM product_events
WHERE UserId != '' AND Timestamp >= {to:DateTime64(9)} - INTERVAL 1 DAY AND Timestamp < {to:DateTime64(9)} AND ` + productFilterScope

	wauSQL = `SELECT uniqExact(UserId) AS "Weekly active users"
FROM product_events
WHERE UserId != '' AND Timestamp >= {to:DateTime64(9)} - INTERVAL 7 DAY AND Timestamp < {to:DateTime64(9)} AND ` + productFilterScope

	mauSQL = `SELECT uniqExact(UserId) AS "Monthly active users"
FROM product_events
WHERE UserId != '' AND Timestamp >= {to:DateTime64(9)} - INTERVAL 30 DAY AND Timestamp < {to:DateTime64(9)} AND ` + productFilterScope

	stickinessSQL = `SELECT round(uniqExactIf(UserId, Timestamp >= {to:DateTime64(9)} - INTERVAL 1 DAY) / nullIf(uniqExact(UserId), 0), 4) AS "DAU / MAU"
FROM product_events
WHERE UserId != '' AND Timestamp >= {to:DateTime64(9)} - INTERVAL 30 DAY AND Timestamp < {to:DateTime64(9)} AND ` + productFilterScope

	eventsSQL = `SELECT count() AS "Events"
FROM product_events
WHERE ` + productScope

	eventsPerUserSQL = `SELECT round(count() / nullIf(uniqExactIf(UserId, UserId != ''), 0), 1) AS "Events per active user"
FROM product_events
WHERE ` + productScope

	recordsWrittenSQL = `SELECT count() AS "Records written"
FROM product_events
WHERE match(EventName, ` + crudEventRE + `) AND ` + productScope
)

// The headline figures' trends: one point per UTC day of the range, in the
// figure's own column name, each the same measure as the figure over that day
// (or the window ending on it).
const (
	dauTrendSQL = `SELECT toDate(Timestamp) AS day, uniqExact(UserId) AS "Daily active users"
FROM product_events
WHERE ` + activeUserScope + `
GROUP BY day ORDER BY day`

	eventsTrendSQL = `SELECT toDate(Timestamp) AS day, count() AS "Events"
FROM product_events
WHERE ` + productScope + `
GROUP BY day ORDER BY day`

	eventsPerUserTrendSQL = `SELECT toDate(Timestamp) AS day, round(count() / nullIf(uniqExactIf(UserId, UserId != ''), 0), 1) AS "Events per active user"
FROM product_events
WHERE ` + productScope + `
GROUP BY day ORDER BY day`

	recordsWrittenTrendSQL = `SELECT toDate(Timestamp) AS day, count() AS "Records written"
FROM product_events
WHERE match(EventName, ` + crudEventRE + `) AND ` + productScope + `
GROUP BY day ORDER BY day`
)

// rollingSQL is one row per UTC day of the range over each signed-in user's
// active days in the window of days ending that day: every (user, day seen)
// counts toward that day and the days-1 after it. The read starts at the
// start of the day days-1 days before the range's first day (whatever the
// start's time of day, so the first day's window is whole), and a day is drawn
// only when it starts before the range's exclusive end (a range ending at
// midnight draws no point for the day it ends on). offset 0 is the day itself.
func rollingSQL(days int, columns string) string {
	return fmt.Sprintf(`SELECT addDays(seen, offset) AS day, %s
FROM (
    SELECT DISTINCT UserId, toDate(Timestamp) AS seen
    FROM product_events
    WHERE UserId != '' AND Timestamp >= toStartOfDay({from:DateTime64(9)}) - INTERVAL %d DAY AND Timestamp < {to:DateTime64(9)} AND %s
)
ARRAY JOIN range(%d) AS offset
WHERE day >= toDate({from:DateTime64(9)}) AND toDateTime(day) < {to:DateTime64(9)}
GROUP BY day ORDER BY day`, columns, days-1, productFilterScope, days)
}

// rollingActiveTrendSQL is the distinct users active in the days ending each
// day: WAU's trend at 7, MAU's at 30.
func rollingActiveTrendSQL(days int, column string) string {
	return rollingSQL(days, fmt.Sprintf("uniqExact(UserId) AS %q", column))
}

// stickinessTrendSQL is each day's active users over the 30 days ending it.
var stickinessTrendSQL = rollingSQL(30, `round(uniqExactIf(UserId, offset = 0) / nullIf(uniqExact(UserId), 0), 4) AS "DAU / MAU"`)

// newUsersTrendSQL is each day's users first seen that day, by the same
// first-seen rule as the figure.
var newUsersTrendSQL = `SELECT toDate(first_seen) AS day, count() AS "New users"
FROM (
    SELECT UserId, min(Timestamp) AS first_seen
    FROM product_events
    WHERE UserId != '' AND ` + firstSeenScope + ` AND ` + productFilterScope + `
    GROUP BY UserId
)
WHERE first_seen >= {from:DateTime64(9)}
GROUP BY day ORDER BY day`

// newUsersSQL counts the users first seen in the range, by the same first-seen
// rule as new-vs-returning.
var newUsersSQL = `SELECT countIf(first_seen >= {from:DateTime64(9)}) AS "New users"
FROM (
    SELECT UserId, min(Timestamp) AS first_seen
    FROM product_events
    WHERE UserId != '' AND ` + firstSeenScope + ` AND ` + productFilterScope + `
    GROUP BY UserId
)`

// activeUsersOverTimeSQL is each day's active users and the rolling 7-day
// active users ending that day, over the same calendar-day bounds as the
// headline trends (rollingSQL): the lookback starts at the start of the day
// six days before the range's first day, and a day starting at or after the
// range's exclusive end is not drawn.
var activeUsersOverTimeSQL = rollingSQL(7, `uniqExact(UserId) AS "Weekly active (rolling 7 days)",
       uniqExactIf(UserId, offset = 0) AS "Daily active"`)

var newVsReturningSQL = `SELECT a.day AS day,
       countIf(a.day = f.first_day) AS "New",
       countIf(a.day > f.first_day) AS "Returning"
FROM (
    SELECT toStartOfDay(Timestamp) AS day, UserId
    FROM product_events
    WHERE ` + activeUserScope + `
    GROUP BY day, UserId
) AS a
INNER JOIN (
    SELECT UserId, toStartOfDay(min(Timestamp)) AS first_day
    FROM product_events
    WHERE UserId != '' AND ` + firstSeenScope + ` AND ` + productFilterScope + `
    GROUP BY UserId
) AS f ON a.UserId = f.UserId
GROUP BY day ORDER BY day`

// cohortsFrom is every signed-in user's cohort (the Monday of their first
// event in the first-seen window) and the weeks they were active, limited to
// the retentionWeeks cohorts before the end of the range.
var cohortsFrom = `FROM (
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

// retained is the share of a cohort active again w weeks after its own, or
// NULL while that week is still to come (so a young cohort does not read as
// churned). scale 100 gives a percentage, 1 a fraction.
func retained(w, scale, digits int) string {
	return fmt.Sprintf("if(cohort + INTERVAL %d WEEK <= toMonday({to:DateTime64(9)}), round(%d * countIf(has(weeks, cohort + INTERVAL %d WEEK)) / count(), %d), NULL)", w, scale, w, digits)
}

// weeklyRetentionSQL is one row per weekly cohort, named by its week and its
// size, and for each later week the share of the cohort active in it (a
// fraction; the panel's unit is percent), drawn as a heatmap.
func weeklyRetentionSQL() string {
	var weeks strings.Builder
	for w := 1; w < retentionWeeks; w++ {
		fmt.Fprintf(&weeks, ",\n       %s AS \"W%d\"", retained(w, 1, 4), w)
	}
	// The cohort is text, so the rows are a heatmap's rows rather than a time
	// series; its size rides in the label, so every numeric column is a week.
	return `SELECT concat(toString(cohort), ' · ', toString(count()), if(count() = 1, ' user', ' users')) AS "Cohort (week of)"` +
		weeks.String() + "\n" + cohortsFrom
}

// retentionWeeksDrawn are the weeks the retention curve draws.
var retentionWeeksDrawn = []int{1, 2, 4, 8}

// retentionCurveSQL is the retention table as a time series: the cohort week
// on the time axis and a line per drawn week, as fractions (unit percent).
func retentionCurveSQL() string {
	var weeks strings.Builder
	for _, w := range retentionWeeksDrawn {
		fmt.Fprintf(&weeks, ",\n       %s AS \"Week %d\"", retained(w, 1, 4), w)
	}
	return `SELECT cohort` + weeks.String() + "\n" + cohortsFrom
}

const recordsByEntitySQL = `SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t,
       ` + crudEntity + ` AS entity,
       count() AS records
FROM product_events
WHERE match(EventName, ` + crudEventRE + `) AND ` + productScope + `
GROUP BY t, entity ORDER BY t`

const actionsPerEntitySQL = `SELECT ` + crudEntity + ` AS entity,
       countIf(endsWith(EventName, '_created')) AS "Created",
       countIf(endsWith(EventName, '_updated')) AS "Updated",
       countIf(endsWith(EventName, '_deleted')) AS "Deleted"
FROM product_events
WHERE match(EventName, ` + crudEventRE + `) AND ` + productScope + `
GROUP BY entity ORDER BY count() DESC LIMIT 20`

const topPagesSQL = `SELECT Props['route'] AS route,
       countIf(UserId != '') AS "Signed in",
       countIf(UserId = '') AS "Anonymous"
FROM product_events
WHERE EventName = 'page_viewed' AND ` + productScope + `
GROUP BY route ORDER BY count() DESC LIMIT 15`

const eventsByNameSQL = `SELECT EventName AS event,
       countIf(UserId != '') AS "Signed in",
       countIf(UserId = '') AS "Anonymous"
FROM product_events
WHERE ` + productScope + `
GROUP BY EventName ORDER BY count() DESC LIMIT 20`

const mostActiveUsersSQL = `SELECT UserId AS "User", count() AS "Events",
       uniqExact(toDate(Timestamp)) AS "Active days",
       uniqExact(EventName) AS "Distinct events",
       formatDateTime(max(Timestamp), '%Y-%m-%d %H:%i') AS "Last seen (UTC)"
FROM product_events
WHERE ` + activeUserScope + `
GROUP BY UserId ORDER BY count() DESC, UserId LIMIT 25`
